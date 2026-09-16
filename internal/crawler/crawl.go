package crawler

import (
	"context"
	"net/url"
)

type crawlJob struct {
	URL   *url.URL
	Depth int
}

func (i *Inspector) Crawl(ctx context.Context) (result CrawlResult, err error) {
	session := newCrawlSession(i.config.MaxPages)
	result = CrawlResult{
		StartURL:     i.startURL.String(),
		MaxDepth:     i.config.MaxDepth,
		MaxPages:     i.config.MaxPages,
		MaxRedirects: i.config.MaxRedirects,
		DepthByURL:   map[string]int{i.startURL.String(): 0},
		SourcesByURL: make(map[string][]string),
	}
	defer func() {
		result.PagesChecked = session.pagesChecked
		result.MaxPagesReached = session.maxPagesReached
		result.Problems = collectProblems(result.Pages, result.SourcesByURL)
	}()

	frontier := []crawlJob{{URL: i.startURL, Depth: 0}}
	visited := make(map[string]int)
	expandedAt := make(map[string]int)
	sourceSets := make(map[string]map[string]struct{})
	countedLinkDocuments := make(map[string]struct{})

	for len(frontier) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		last := len(frontier) - 1
		job := frontier[last]
		frontier = frontier[:last]
		currentURL := job.URL.String()

		bestDepth, exists := result.DepthByURL[currentURL]
		if !exists || bestDepth != job.Depth {
			continue
		}

		pagePosition, alreadyVisited := visited[currentURL]
		if !alreadyVisited {
			page, pageChecked, inspectErr := i.inspectURL(
				ctx,
				job.URL,
				job.Depth,
				session,
			)
			if !pageChecked {
				if inspectErr != nil {
					return result, inspectErr
				}
				continue
			}

			pagePosition = len(result.Pages)
			visited[currentURL] = pagePosition
			result.Pages = append(result.Pages, page)
			if _, counted := countedLinkDocuments[page.FinalURL]; page.HTMLParsed && !counted {
				countedLinkDocuments[page.FinalURL] = struct{}{}
				result.LinksDiscovered += page.LinksDiscovered
			}
			recordSources(&result, page, sourceSets)
			if inspectErr != nil {
				return result, inspectErr
			}

			if err := ctx.Err(); err != nil {
				return result, err
			}
		} else if job.Depth < result.Pages[pagePosition].Depth {
			result.Pages[pagePosition].Depth = job.Depth
		}

		page := result.Pages[pagePosition]
		previousExpansionDepth, wasExpanded := expandedAt[currentURL]
		if wasExpanded && previousExpansionDepth <= job.Depth {
			continue
		}
		expandedAt[currentURL] = job.Depth

		children := make([]crawlJob, 0, len(page.Links))
		for _, discovered := range page.Links {
			if discovered.Kind != LinkInternal {
				continue
			}

			childDepth := job.Depth + 1
			knownDepth, wasDiscovered := result.DepthByURL[discovered.URL]
			if wasDiscovered && knownDepth <= childDepth {
				continue
			}

			result.DepthByURL[discovered.URL] = childDepth
			if childPosition, childVisited := visited[discovered.URL]; childVisited {
				result.Pages[childPosition].Depth = childDepth
			}

			if childDepth > i.config.MaxDepth {
				continue
			}

			childURL, parseErr := url.Parse(discovered.URL)
			if parseErr != nil {
				continue
			}
			children = append(children, crawlJob{URL: childURL, Depth: childDepth})
		}

		for childIndex := len(children) - 1; childIndex >= 0; childIndex-- {
			frontier = append(frontier, children[childIndex])
		}
	}

	return result, nil
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
