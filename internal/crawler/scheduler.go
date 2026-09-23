package crawler

import (
	"context"
	"net/url"
)

type crawlScheduler struct {
	inspector *Inspector
	result    *CrawlResult
	session   *crawlSession
	jobs      chan<- crawlTask
	results   <-chan crawlWorkerResult

	frontier             []crawlJob
	completed            map[string]int
	running              map[string]struct{}
	unavailable          map[string]struct{}
	expandedAt           map[string]int
	sourceSets           map[string]map[string]struct{}
	countedLinkDocuments map[string]struct{}
	dispatchOrder        map[string]int
	nextSequence         int
	active               int
	pageLimitReached     bool
}

func newCrawlScheduler(
	inspector *Inspector,
	result *CrawlResult,
	session *crawlSession,
	jobs chan<- crawlTask,
	results <-chan crawlWorkerResult,
) *crawlScheduler {
	return &crawlScheduler{
		inspector:            inspector,
		result:               result,
		session:              session,
		jobs:                 jobs,
		results:              results,
		frontier:             []crawlJob{{URL: inspector.startURL, Depth: 0}},
		completed:            make(map[string]int),
		running:              make(map[string]struct{}),
		unavailable:          make(map[string]struct{}),
		expandedAt:           make(map[string]int),
		sourceSets:           make(map[string]map[string]struct{}),
		countedLinkDocuments: make(map[string]struct{}),
		dispatchOrder:        make(map[string]int),
	}
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
	for len(s.frontier) > 0 {
		last := len(s.frontier) - 1
		job := s.frontier[last]
		s.frontier = s.frontier[:last]
		key := job.URL.String()

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
		if s.pageLimitReached && !s.session.hasFetchEntry(key) {
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

	if !workerResult.PageChecked {
		s.pageLimitReached = true
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

	children := make([]crawlJob, 0, len(page.Links))
	for _, discovered := range page.Links {
		if discovered.Kind != LinkInternal {
			continue
		}

		childDepth := depth + 1
		knownDepth, known := s.result.DepthByURL[discovered.URL]
		if known && knownDepth <= childDepth {
			continue
		}

		s.result.DepthByURL[discovered.URL] = childDepth
		childPosition, childDone := s.completed[discovered.URL]
		if childDone {
			s.result.Pages[childPosition].Depth = childDepth
		}

		if childDepth > s.inspector.config.MaxDepth {
			continue
		}
		if _, blocked := s.unavailable[discovered.URL]; blocked {
			continue
		}
		childURL, err := url.Parse(discovered.URL)
		if err != nil {
			continue
		}
		if s.pageLimitReached && !childDone &&
			!s.session.hasFetchEntry(discovered.URL) {
			continue
		}
		children = append(children, crawlJob{URL: childURL, Depth: childDepth})
	}

	for index := len(children) - 1; index >= 0; index-- {
		s.frontier = append(s.frontier, children[index])
	}
}
