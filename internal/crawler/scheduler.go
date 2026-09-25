package crawler

import (
	"container/list"
	"context"
	"net/url"
)

type expansionFrame struct {
	pagePosition int
	depth        int
	nextLink     int
}

type frameChild struct {
	job           crawlJob
	alreadyQueued bool
}

type crawlScheduler struct {
	inspector *Inspector
	result    *CrawlResult
	jobs      chan<- crawlTask
	results   <-chan crawlWorkerResult

	frontier             *list.List // crawlJob or expansionFrame; Back has DFS priority
	pending              map[string]*list.Element
	queuedJobs           int
	completed            map[string]int
	running              map[string]struct{}
	unavailable          map[string]struct{}
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
		expandedAt:           make(map[string]int),
		sourceSets:           make(map[string]map[string]struct{}),
		countedLinkDocuments: make(map[string]struct{}),
		dispatchOrder:        make(map[string]int),
	}
	startJob := crawlJob{URL: inspector.startURL, Depth: 0}
	element := scheduler.frontier.PushBack(startJob)
	scheduler.pending[inspector.startURL.String()] = element
	scheduler.queuedJobs = 1
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
		var job crawlJob
		switch item := element.Value.(type) {
		case crawlJob:
			job = item
			s.frontier.Remove(element)
			delete(s.pending, job.URL.String())
			s.queuedJobs--
		case expansionFrame:
			s.fillFrame(element)
			if s.frontier.Back() != element {
				continue
			}
			// Lower-priority siblings may occupy every ready slot. Hand the next
			// child of this higher-priority frame directly to a free worker.
			var pendingElement *list.Element
			var available bool
			job, pendingElement, available = s.nextFrameJob(element, nil)
			if !available {
				continue
			}
			if pendingElement != nil {
				s.frontier.Remove(pendingElement)
				delete(s.pending, job.URL.String())
				s.queuedJobs--
			} else {
				s.queueLimitReached = true
			}
		}

		key := job.URL.String()
		bestDepth, known := s.result.DepthByURL[key]
		if !known || bestDepth > s.inspector.config.MaxDepth {
			continue
		}
		job.Depth = bestDepth
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
	if previousDepth, expanded := s.expandedAt[page.URL]; expanded && previousDepth <= depth {
		return
	}
	s.expandedAt[page.URL] = depth
	if len(page.Links) == 0 {
		return
	}

	frame := s.frontier.PushBack(expansionFrame{pagePosition: pagePosition, depth: depth})
	s.fillFrame(frame)
}

// fillFrame stages new jobs within MaxQueue and promotes already queued jobs
// discovered through this page, preserving DFS order without extra slots.
func (s *crawlScheduler) fillFrame(element *list.Element) {
	availableSlots := s.inspector.config.MaxQueue - s.queuedJobs
	children := make([]frameChild, 0)
	candidates := make(map[string]struct{})
	for {
		job, pendingElement, available := s.nextFrameJob(element, candidates)
		if !available {
			break
		}
		if pendingElement == nil && availableSlots == 0 {
			frame := element.Value.(expansionFrame)
			frame.nextLink--
			element.Value = frame
			s.queueLimitReached = true
			break
		}
		candidates[job.URL.String()] = struct{}{}
		if pendingElement != nil {
			s.frontier.Remove(pendingElement)
			delete(s.pending, job.URL.String())
		} else {
			availableSlots--
		}
		children = append(children, frameChild{job: job, alreadyQueued: pendingElement != nil})
	}
	for index := len(children) - 1; index >= 0; index-- {
		child := children[index]
		queued := s.frontier.PushBack(child.job)
		s.pending[child.job.URL.String()] = queued
		if !child.alreadyQueued {
			s.queuedJobs++
		}
	}
	if s.queuedJobs > s.peakQueueSize {
		s.peakQueueSize = s.queuedJobs
	}
}

// nextFrameJob advances one page cursor until it finds a runnable child. All
// links remain in PageResult.Links, so queue pressure cannot discard a URL.
func (s *crawlScheduler) nextFrameJob(
	element *list.Element,
	candidates map[string]struct{},
) (crawlJob, *list.Element, bool) {
	frame := element.Value.(expansionFrame)
	page := s.result.Pages[frame.pagePosition]
	for frame.nextLink < len(page.Links) {
		discovered := page.Links[frame.nextLink]
		frame.nextLink++
		element.Value = frame
		if discovered.Kind != LinkInternal {
			continue
		}

		childDepth := frame.depth + 1
		bestDepth, known := s.result.DepthByURL[discovered.URL]
		if !known || childDepth < bestDepth {
			bestDepth = childDepth
			s.result.DepthByURL[discovered.URL] = bestDepth
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
		if childDone {
			if expandedDepth, expanded := s.expandedAt[discovered.URL]; expanded && expandedDepth <= bestDepth {
				continue
			}
		}
		if _, inFlight := s.running[discovered.URL]; inFlight {
			continue
		}
		if _, candidate := candidates[discovered.URL]; candidate {
			continue
		}
		if pendingElement, pending := s.pending[discovered.URL]; pending {
			pendingJob := pendingElement.Value.(crawlJob)
			if bestDepth < pendingJob.Depth {
				pendingJob.Depth = bestDepth
				pendingElement.Value = pendingJob
			}
			return pendingJob, pendingElement, true
		}
		childURL, err := url.Parse(discovered.URL)
		if err != nil {
			continue
		}
		return crawlJob{URL: childURL, Depth: bestDepth}, nil, true
	}
	s.frontier.Remove(element)
	return crawlJob{}, nil, false
}
