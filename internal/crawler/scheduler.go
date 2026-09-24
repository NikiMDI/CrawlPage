package crawler

import (
	"container/list"
	"context"
	"net/url"
)

type crawlScheduler struct {
	inspector *Inspector
	result    *CrawlResult
	jobs      chan<- crawlTask
	results   <-chan crawlWorkerResult

	frontier             *list.List
	pending              map[string]*list.Element
	completed            map[string]int
	running              map[string]struct{}
	unavailable          map[string]struct{}
	queueSkipped         map[string]struct{}
	expandedAt           map[string]int
	sourceSets           map[string]map[string]struct{}
	countedLinkDocuments map[string]struct{}
	dispatchOrder        map[string]int
	nextSequence         int
	active               int
	peakQueueSize        int
	queueLimitReached    bool
}

func newCrawlScheduler(
	inspector *Inspector,
	result *CrawlResult,
	jobs chan<- crawlTask,
	results <-chan crawlWorkerResult,
) *crawlScheduler {
	scheduler := &crawlScheduler{
		inspector:            inspector,
		result:               result,
		jobs:                 jobs,
		results:              results,
		frontier:             list.New(),
		pending:              make(map[string]*list.Element),
		completed:            make(map[string]int),
		running:              make(map[string]struct{}),
		unavailable:          make(map[string]struct{}),
		queueSkipped:         make(map[string]struct{}),
		expandedAt:           make(map[string]int),
		sourceSets:           make(map[string]map[string]struct{}),
		countedLinkDocuments: make(map[string]struct{}),
		dispatchOrder:        make(map[string]int),
	}
	startJob := crawlJob{URL: inspector.startURL, Depth: 0}
	element := scheduler.frontier.PushBack(startJob)
	scheduler.pending[inspector.startURL.String()] = element
	scheduler.peakQueueSize = 1
	return scheduler
}

func (s *crawlScheduler) run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		for s.active < s.inspector.config.Concurrency {
			job, available := s.nextRunnableJob()
			if !available {
				break
			}

			task := crawlTask{Job: job, Sequence: s.nextSequence}
			select {
			case s.jobs <- task:
				key := job.URL.String()
				s.running[key] = struct{}{}
				s.dispatchOrder[key] = task.Sequence
				s.nextSequence++
				s.active++
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if s.active == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case workerResult := <-s.results:
			s.active--
			delete(s.running, workerResult.Task.Job.URL.String())
			if err := s.accept(workerResult); err != nil {
				return err
			}
		}
	}
}

func (s *crawlScheduler) nextRunnableJob() (crawlJob, bool) {
	for s.frontier.Len() > 0 {
		element := s.frontier.Back()
		job := element.Value.(crawlJob)
		s.frontier.Remove(element)
		key := job.URL.String()
		delete(s.pending, key)

		bestDepth, known := s.result.DepthByURL[key]
		if !known || bestDepth != job.Depth {
			continue
		}
		if _, blocked := s.unavailable[key]; blocked {
			continue
		}
		if pagePosition, done := s.completed[key]; done {
			if job.Depth < s.result.Pages[pagePosition].Depth {
				s.result.Pages[pagePosition].Depth = job.Depth
			}
			s.expand(pagePosition, job.Depth)
			continue
		}
		if _, inFlight := s.running[key]; inFlight {
			continue
		}
		return job, true
	}
	return crawlJob{}, false
}

func (s *crawlScheduler) accept(workerResult crawlWorkerResult) error {
	job := workerResult.Task.Job
	key := job.URL.String()
	if workerResult.Err != nil {
		return workerResult.Err
	}

	if !workerResult.PageAccepted {
		s.unavailable[key] = struct{}{}
		return nil
	}

	page := workerResult.Page
	if bestDepth, exists := s.result.DepthByURL[key]; exists && bestDepth < page.Depth {
		page.Depth = bestDepth
	}

	pagePosition := len(s.result.Pages)
	s.completed[key] = pagePosition
	s.result.Pages = append(s.result.Pages, page)

	if _, counted := s.countedLinkDocuments[page.FinalURL]; page.HTMLParsed && !counted {
		s.countedLinkDocuments[page.FinalURL] = struct{}{}
		s.result.LinksDiscovered += page.LinksDiscovered
	}
	recordSources(s.result, page, s.sourceSets)
	s.expand(pagePosition, page.Depth)

	return nil
}

func (s *crawlScheduler) expand(pagePosition int, depth int) {
	page := s.result.Pages[pagePosition]
	key := page.URL
	if previousDepth, expanded := s.expandedAt[key]; expanded && previousDepth <= depth {
		return
	}
	s.expandedAt[key] = depth

	availableSlots := s.inspector.config.MaxQueue - s.frontier.Len()
	if availableSlots < 0 {
		availableSlots = 0
	}
	if availableSlots > len(page.Links) {
		availableSlots = len(page.Links)
	}
	children := make([]crawlJob, 0, availableSlots)
	candidatePositions := make(map[string]int, availableSlots)
	for _, discovered := range page.Links {
		if discovered.Kind != LinkInternal {
			continue
		}

		childDepth := depth + 1
		knownDepth, known := s.result.DepthByURL[discovered.URL]
		bestDepth := childDepth
		if known && knownDepth < bestDepth {
			bestDepth = knownDepth
		}
		if !known || childDepth < knownDepth {
			s.result.DepthByURL[discovered.URL] = childDepth
		}
		childPosition, childDone := s.completed[discovered.URL]
		if childDone && bestDepth < s.result.Pages[childPosition].Depth {
			s.result.Pages[childPosition].Depth = bestDepth
		}

		if bestDepth > s.inspector.config.MaxDepth {
			continue
		}
		if _, blocked := s.unavailable[discovered.URL]; blocked {
			continue
		}
		childURL, err := url.Parse(discovered.URL)
		if err != nil {
			continue
		}
		if childDone {
			if expandedDepth, expanded := s.expandedAt[discovered.URL]; expanded && expandedDepth <= bestDepth {
				continue
			}
		}
		if _, inFlight := s.running[discovered.URL]; inFlight {
			continue
		}
		if pendingElement, pending := s.pending[discovered.URL]; pending {
			pendingJob := pendingElement.Value.(crawlJob)
			if bestDepth < pendingJob.Depth {
				pendingJob.Depth = bestDepth
				pendingElement.Value = pendingJob
			}
			continue
		}
		if candidatePosition, candidate := candidatePositions[discovered.URL]; candidate {
			if bestDepth < children[candidatePosition].Depth {
				children[candidatePosition].Depth = bestDepth
			}
			continue
		}
		// Do not block the scheduler here: it must remain able to receive active
		// worker results. Overflow is explicit in the report, and the URL may be
		// admitted if another page discovers it after a slot becomes free.
		if s.frontier.Len()+len(children) >= s.inspector.config.MaxQueue {
			s.queueLimitReached = true
			s.queueSkipped[discovered.URL] = struct{}{}
			continue
		}
		delete(s.queueSkipped, discovered.URL)
		candidatePositions[discovered.URL] = len(children)
		children = append(children, crawlJob{URL: childURL, Depth: bestDepth})
	}

	for index := len(children) - 1; index >= 0; index-- {
		job := children[index]
		element := s.frontier.PushBack(job)
		s.pending[job.URL.String()] = element
	}
	if s.frontier.Len() > s.peakQueueSize {
		s.peakQueueSize = s.frontier.Len()
	}
}
