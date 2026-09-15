package crawler

import (
	"context"
	"net/url"
)

type crawlJob struct {
	URL   *url.URL
	Depth int
}

func (i *Inspector) Crawl(ctx context.Context) (CrawlResult, error) {
	result := CrawlResult{
		StartURL:     i.startURL.String(),
		MaxDepth:     i.config.MaxDepth,
		MaxPages:     i.config.MaxPages,
		DepthByURL:   map[string]int{i.startURL.String(): 0},
		SourcesByURL: make(map[string][]string),
	}

	frontier := []crawlJob{{URL: i.startURL, Depth: 0}}
	visited := make(map[string]int)
	expandedAt := make(map[string]int)
	sourceSets := make(map[string]map[string]struct{})

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
			if len(result.Pages) >= i.config.MaxPages {
				result.MaxPagesReached = true
				continue
			}

			page, fetchErr := i.inspectURL(ctx, job.URL, job.Depth)
			page.FetchError = fetchErr
			pagePosition = len(result.Pages)
			visited[currentURL] = pagePosition
			result.Pages = append(result.Pages, page)
			result.LinksDiscovered += page.LinksDiscovered
			recordSources(&result, page, sourceSets)

			if err := ctx.Err(); err != nil {
				return result, err
			}
		} else if job.Depth < result.Pages[pagePosition].Depth {
			result.Pages[pagePosition].Depth = job.Depth
		}

		previousExpansionDepth, wasExpanded := expandedAt[currentURL]
		if wasExpanded && previousExpansionDepth <= job.Depth {
			continue
		}
		expandedAt[currentURL] = job.Depth

		page := result.Pages[pagePosition]
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
