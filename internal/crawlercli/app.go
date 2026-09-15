package crawlercli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

// Run parses CLI arguments, runs the crawler, writes its report, and returns
// the process exit code. Keeping this logic outside package main makes the CLI
// testable without starting a child process.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("crawler", flag.ContinueOnError)
	flags.SetOutput(stderr)

	startURL := flags.String("url", "", "absolute start URL")
	timeout := flags.Duration("timeout", 2*time.Second, "timeout for the HTTP request")
	maxDepth := flags.Int("depth", 3, "maximum crawl depth; the start page has depth 0")
	maxPages := flags.Int("max-pages", 100, "maximum number of internal URLs to check")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	inspector, err := crawler.New(crawler.Config{
		StartURL:       *startURL,
		RequestTimeout: *timeout,
		MaxDepth:       *maxDepth,
		MaxPages:       *maxPages,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}

	result, err := inspector.Crawl(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "crawl failed: %v\n", err)
		return 1
	}

	writeReport(stdout, result)
	return 0
}

func writeReport(output io.Writer, result crawler.CrawlResult) {
	unique := make(map[string]struct{})
	resultCounts := make(map[crawler.ResultKind]int)
	internalCount := 0
	externalCount := 0
	skippedCount := 0
	for _, page := range result.Pages {
		resultCounts[page.ResultKind]++
		skippedCount += len(page.Skipped)
		for _, discovered := range page.Links {
			unique[discovered.URL] = struct{}{}
			switch discovered.Kind {
			case crawler.LinkInternal:
				internalCount++
			case crawler.LinkExternal:
				externalCount++
			}
		}
	}

	fmt.Fprintln(output, "Stage 3: HTTP result classification")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Level 1 — summary")
	fmt.Fprintf(output, "Start URL:          %s\n", result.StartURL)
	fmt.Fprintf(output, "Maximum depth:      %d\n", result.MaxDepth)
	fmt.Fprintf(output, "Maximum pages:      %d\n", result.MaxPages)
	fmt.Fprintf(output, "Max pages reached:  %t\n", result.MaxPagesReached)
	fmt.Fprintf(output, "Pages checked:      %d\n", len(result.Pages))
	fmt.Fprintf(output, "Links discovered:   %d\n", result.LinksDiscovered)
	fmt.Fprintf(output, "Unique HTTP links:  %d\n", len(unique))
	fmt.Fprintf(output, "Internal links:     %d\n", internalCount)
	fmt.Fprintf(output, "External links:     %d\n", externalCount)
	fmt.Fprintf(output, "Skipped links:      %d\n", skippedCount)
	fmt.Fprintf(output, "Successful:         %d\n", resultCounts[crawler.ResultSuccess])
	fmt.Fprintf(output, "Redirects:          %d\n", resultCounts[crawler.ResultRedirect])
	fmt.Fprintf(output, "Broken links:       %d\n", len(result.Problems))
	fmt.Fprintf(output, "HTTP 4xx:           %d\n", resultCounts[crawler.ResultHTTP4XX])
	fmt.Fprintf(output, "HTTP 5xx:           %d\n", resultCounts[crawler.ResultHTTP5XX])
	fmt.Fprintf(output, "Timeouts:           %d\n", resultCounts[crawler.ResultTimeout])
	fmt.Fprintf(output, "Network errors:     %d\n", resultCounts[crawler.ResultNetworkError])
	fmt.Fprintf(output, "Other HTTP statuses: %d\n", resultCounts[crawler.ResultOtherHTTPStatus])

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Level 2 — details")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "DFS visit order")
	if len(result.Pages) == 0 {
		fmt.Fprintln(output, "No pages were checked.")
		return
	}

	for pageIndex, page := range result.Pages {
		fmt.Fprintln(output)
		fmt.Fprintf(output, "PAGE %d\n", pageIndex+1)
		fmt.Fprintf(output, "Depth:  %d\n", page.Depth)
		if page.FetchDepth != page.Depth {
			fmt.Fprintf(output, "Fetched at depth: %d\n", page.FetchDepth)
		}
		fmt.Fprintf(output, "URL:    %s\n", page.URL)
		fmt.Fprintf(output, "Result: %s\n", page.ResultKind)
		if page.Status != "" {
			fmt.Fprintf(output, "Status: %s\n", page.Status)
		}
		if page.Error != "" {
			fmt.Fprintf(output, "Error:  %s\n", page.Error)
		}

		for _, discovered := range page.Links {
			fmt.Fprintf(
				output,
				"Link:   [%s] %s -> %s\n",
				discovered.Kind,
				discovered.RawHref,
				discovered.URL,
			)
		}
		for _, skipped := range page.Skipped {
			fmt.Fprintf(
				output,
				"Link:   [SKIPPED] %s (%s)\n",
				skipped.RawHref,
				strings.TrimSpace(skipped.Reason),
			)
		}
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Broken link details")
	if len(result.Problems) == 0 {
		fmt.Fprintln(output, "No broken links were found.")
		return
	}

	for problemIndex, problem := range result.Problems {
		fmt.Fprintln(output)
		fmt.Fprintf(output, "PROBLEM %d\n", problemIndex+1)
		fmt.Fprintf(output, "URL:    %s\n", problem.URL)
		fmt.Fprintf(output, "Result: %s\n", problem.Kind)
		if problem.Status != "" {
			fmt.Fprintf(output, "Status: %s\n", problem.Status)
		}
		if problem.Error != "" {
			fmt.Fprintf(output, "Error:  %s\n", problem.Error)
		}
		fmt.Fprintln(output, "Found on:")
		if len(problem.Sources) == 0 {
			fmt.Fprintln(output, "- [start URL]")
			continue
		}
		for _, source := range problem.Sources {
			fmt.Fprintf(output, "- %s\n", source)
		}
	}
}
