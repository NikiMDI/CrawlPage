package crawler

import (
	"context"
	"net/url"
	"sort"
	"sync"
	"time"
)

type crawlJob struct {
	URL   *url.URL
	Depth int
}

func (i *Inspector) Crawl(ctx context.Context) (result CrawlResult, err error) {
	startedAt := time.Now()
	session := newCrawlSession(i.config.MaxPages)
	result = CrawlResult{
		StartURL:     i.startURL.String(),
		MaxDepth:     i.config.MaxDepth,
		MaxPages:     i.config.MaxPages,
		MaxRedirects: i.config.MaxRedirects,
		Concurrency:  i.config.Concurrency,
		MaxQueue:     i.config.MaxQueue,
		MaxHTMLBytes: i.config.MaxHTMLBytes,
		DepthByURL:   map[string]int{i.startURL.String(): 0},
		SourcesByURL: make(map[string][]string),
	}

	workerContext, cancelWorkers := context.WithCancel(ctx)
	jobs := make(chan crawlTask)
	workerResults := make(chan crawlWorkerResult, i.config.Concurrency)
	var workers sync.WaitGroup
	for workerIndex := 0; workerIndex < i.config.Concurrency; workerIndex++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			i.runWorker(workerContext, session, jobs, workerResults)
		}()
	}

	scheduler := newCrawlScheduler(i, &result, jobs, workerResults)
	defer func() {
		cancelWorkers()
		close(jobs)
		workers.Wait()

		result.PagesChecked, result.MaxPagesReached = session.stats()
		result.PeakQueueSize = scheduler.peakQueueSize
		result.QueueLimitReached = scheduler.queueLimitReached
		result.QueueLinksSkipped = len(scheduler.queueSkipped)
		sort.SliceStable(result.Pages, func(left, right int) bool {
			return scheduler.dispatchOrder[result.Pages[left].URL] <
				scheduler.dispatchOrder[result.Pages[right].URL]
		})
		result.Problems = collectProblems(result.Pages, result.SourcesByURL)
		result.HTMLTooLarge = collectHTMLTooLarge(result.Pages, result.SourcesByURL)
		result.Elapsed = time.Since(startedAt)
	}()

	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, scheduler.run(workerContext)
}

func collectHTMLTooLarge(
	pages []PageResult,
	sourcesByURL map[string][]string,
) []Problem {
	oversized := make([]Problem, 0)
	for _, page := range pages {
		if page.ResultKind != ResultHTMLTooLarge {
			continue
		}

		oversized = append(oversized, Problem{
			URL:     page.URL,
			Kind:    page.ResultKind,
			Status:  page.Status,
			Error:   page.Error,
			Sources: append([]string(nil), sourcesByURL[page.URL]...),
		})
	}
	return oversized
}

func collectProblems(pages []PageResult, sourcesByURL map[string][]string) []Problem {
	problems := make([]Problem, 0)
	for _, page := range pages {
		if !page.ResultKind.IsBroken() {
			continue
		}

		problems = append(problems, Problem{
			URL:     page.URL,
			Kind:    page.ResultKind,
			Status:  page.Status,
			Error:   page.Error,
			Sources: append([]string(nil), sourcesByURL[page.URL]...),
		})
	}
	return problems
}

func recordSources(
	result *CrawlResult,
	page PageResult,
	sourceSets map[string]map[string]struct{},
) {
	for _, discovered := range page.Links {
		sources, exists := sourceSets[discovered.URL]
		if !exists {
			sources = make(map[string]struct{})
			sourceSets[discovered.URL] = sources
		}
		if _, duplicate := sources[discovered.SourceURL]; duplicate {
			continue
		}

		sources[discovered.SourceURL] = struct{}{}
		result.SourcesByURL[discovered.URL] = append(
			result.SourcesByURL[discovered.URL],
			discovered.SourceURL,
		)
	}
}
