package crawlercli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
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
	maxPages := flags.Int("max-pages", 100, "maximum number of unique internal URLs to request")
	maxRedirects := flags.Int("max-redirects", 10, "maximum redirects followed for one URL")
	concurrency := flags.Int("concurrency", 4, "maximum number of concurrent HTTP requests")
	maxHTMLBytes := flags.Int64(
		"max-html-bytes",
		crawler.DefaultMaxHTMLBytes,
		"maximum number of bytes read from one HTML response",
	)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *maxHTMLBytes <= 0 {
		fmt.Fprintln(stderr, "configuration error: maximum HTML bytes must be positive")
		return 2
	}

	inspector, err := crawler.New(crawler.Config{
		StartURL:       *startURL,
		RequestTimeout: *timeout,
		MaxDepth:       *maxDepth,
		MaxPages:       *maxPages,
		MaxRedirects:   *maxRedirects,
		Concurrency:    *concurrency,
		MaxHTMLBytes:   *maxHTMLBytes,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}

	result, err := inspector.Crawl(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "crawl cancelled")
			return 130
		}
		fmt.Fprintf(stderr, "crawl failed: %v\n", err)
		return 1
	}

	writeReport(stdout, result)
	return 0
}

func writeReport(output io.Writer, result crawler.CrawlResult) {
	unique := make(map[string]struct{})
	resultCounts := make(map[crawler.ResultKind]int)
	httpStatusCounts := make(map[int]int)
	internalCount := 0
	externalCount := 0
	skippedCount := 0
	redirectChains := 0
	redirectHops := 0
	externalRedirects := 0
	countedLinkDocuments := make(map[string]struct{})
	for _, page := range result.Pages {
		resultCounts[page.ResultKind]++
		if page.ResultKind == crawler.ResultHTTP4XX ||
			page.ResultKind == crawler.ResultHTTP5XX {
			if statusCode, ok := parseHTTPStatusCode(page.Status); ok {
				httpStatusCounts[statusCode]++
			}
		}
		if len(page.RedirectChain) > 0 {
			redirectChains++
			redirectHops += len(page.RedirectChain)
		}
		if page.RedirectedOutsideScope {
			externalRedirects++
		}
		if !page.HTMLParsed {
			continue
		}
		if _, counted := countedLinkDocuments[page.FinalURL]; counted {
			continue
		}
		countedLinkDocuments[page.FinalURL] = struct{}{}
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

	fmt.Fprintln(output, "Level 1 — summary")
	fmt.Fprintf(output, "Start URL:          %s\n", result.StartURL)
	fmt.Fprintf(output, "Maximum depth:      %d\n", result.MaxDepth)
	fmt.Fprintf(output, "Maximum pages:      %d\n", result.MaxPages)
	fmt.Fprintf(output, "Maximum redirects:  %d\n", result.MaxRedirects)
	fmt.Fprintf(output, "Concurrency:        %d\n", result.Concurrency)
	fmt.Fprintf(output, "Maximum HTML bytes: %d\n", result.MaxHTMLBytes)
	fmt.Fprintf(output, "Elapsed:            %s\n", formatElapsed(result.Elapsed))
	fmt.Fprintf(output, "Max pages reached:  %t\n", result.MaxPagesReached)
	fmt.Fprintf(output, "Pages checked:      %d\n", result.PagesChecked)
	fmt.Fprintf(output, "Links discovered:   %d\n", result.LinksDiscovered)
	fmt.Fprintf(output, "Unique HTTP links:  %d\n", len(unique))
	fmt.Fprintf(output, "Internal links:     %d\n", internalCount)
	fmt.Fprintf(output, "External links:     %d\n", externalCount)
	fmt.Fprintf(output, "Skipped links:      %d\n", skippedCount)
	fmt.Fprintf(output, "Successful:         %d\n", resultCounts[crawler.ResultSuccess])
	fmt.Fprintf(output, "Redirect chains:    %d\n", redirectChains)
	fmt.Fprintf(output, "Redirect hops:      %d\n", redirectHops)
	fmt.Fprintf(output, "External redirects: %d\n", externalRedirects)
	fmt.Fprintf(output, "Redirect errors:    %d\n", resultCounts[crawler.ResultRedirectError])
	fmt.Fprintf(output, "Stopped by max-pages: %d\n", resultCounts[crawler.ResultPageLimit])
	fmt.Fprintf(output, "Broken links:       %d\n", len(result.Problems))
	fmt.Fprintf(output, "HTTP 4xx:           %d\n", resultCounts[crawler.ResultHTTP4XX])
	writeHTTPStatusCounts(output, httpStatusCounts, 400, 499)
	fmt.Fprintf(output, "HTTP 5xx:           %d\n", resultCounts[crawler.ResultHTTP5XX])
	writeHTTPStatusCounts(output, httpStatusCounts, 500, 599)
	fmt.Fprintf(output, "Timeouts:           %d\n", resultCounts[crawler.ResultTimeout])
	fmt.Fprintf(output, "Network errors:     %d\n", resultCounts[crawler.ResultNetworkError])
	fmt.Fprintf(output, "HTML too large:     %d\n", len(result.HTMLTooLarge))
	fmt.Fprintf(output, "Other HTTP statuses: %d\n", resultCounts[crawler.ResultOtherHTTPStatus])

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Level 2 — details")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "DFS-priority scheduling order")
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
		if page.FinalURL != "" && page.FinalURL != page.URL {
			fmt.Fprintf(output, "Final URL: %s\n", page.FinalURL)
		}
		if len(page.RedirectChain) > 0 {
			fmt.Fprintf(output, "Redirect hops: %d\n", len(page.RedirectChain))
		}
		fmt.Fprintf(output, "Result: %s\n", page.ResultKind)
		if page.Status != "" {
			fmt.Fprintf(output, "Status: %s\n", page.Status)
		}
		if page.Error != "" {
			fmt.Fprintf(output, "Error:  %s\n", page.Error)
		}
		if page.StoppedByPageLimit {
			fmt.Fprintln(output, "Stopped: max-pages reached before requesting the final URL")
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
	fmt.Fprintln(output, "Redirect details")
	if redirectChains == 0 {
		fmt.Fprintln(output, "No redirects were found.")
	} else {
		redirectIndex := 0
		for _, page := range result.Pages {
			if len(page.RedirectChain) == 0 {
				continue
			}
			redirectIndex++
			fmt.Fprintln(output)
			fmt.Fprintf(output, "REDIRECT %d\n", redirectIndex)
			fmt.Fprintf(output, "Link:   %s\n", page.URL)
			fmt.Fprintln(output, "Found on:")
			writeSources(output, result.SourcesByURL[page.URL])
			fmt.Fprintln(output, "Chain:")
			for hopIndex, hop := range page.RedirectChain {
				targetURL := hop.TargetURL
				if targetURL == "" {
					targetURL = "[missing Location]"
				}
				fmt.Fprintf(
					output,
					"%d. %s — %s -> %s\n",
					hopIndex+1,
					hop.URL,
					hop.Status,
					targetURL,
				)
			}

			lastHop := page.RedirectChain[len(page.RedirectChain)-1]
			switch {
			case page.RedirectedOutsideScope:
				fmt.Fprintf(
					output,
					"%d. %s — EXTERNAL (not requested)\n",
					len(page.RedirectChain)+1,
					page.FinalURL,
				)
			case page.RedirectTargetSkipped:
				fmt.Fprintf(
					output,
					"%d. %s — SKIPPED (%s)\n",
					len(page.RedirectChain)+1,
					page.FinalURL,
					page.RedirectSkipReason,
				)
			case page.RedirectCycleDetected:
				fmt.Fprintf(
					output,
					"%d. %s — CYCLE (already requested in this chain)\n",
					len(page.RedirectChain)+1,
					page.FinalURL,
				)
			case page.StoppedByPageLimit:
				fmt.Fprintf(
					output,
					"%d. %s — NOT REQUESTED (max-pages reached)\n",
					len(page.RedirectChain)+1,
					page.FinalURL,
				)
			case page.ResultKind == crawler.ResultRedirectError:
				if page.FinalURL != "" && page.FinalURL != lastHop.URL {
					fmt.Fprintf(
						output,
						"%d. %s — NOT REQUESTED\n",
						len(page.RedirectChain)+1,
						page.FinalURL,
					)
				}
			default:
				if page.Status != "" {
					fmt.Fprintf(
						output,
						"%d. %s — %s\n",
						len(page.RedirectChain)+1,
						page.FinalURL,
						page.Status,
					)
				} else {
					fmt.Fprintf(
						output,
						"%d. %s — %s\n",
						len(page.RedirectChain)+1,
						page.FinalURL,
						page.ResultKind,
					)
				}
			}
			fmt.Fprintf(output, "Final result: %s\n", page.ResultKind)
			if page.Error != "" {
				fmt.Fprintf(output, "Error: %s\n", page.Error)
			}
		}
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Broken link details")
	if len(result.Problems) == 0 {
		fmt.Fprintln(output, "No broken links were found.")
	} else {
		for problemIndex, problem := range result.Problems {
			fmt.Fprintln(output)
			fmt.Fprintf(output, "PROBLEM %d\n", problemIndex+1)
			writeProblem(output, problem)
		}
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "HTML too large details")
	if len(result.HTMLTooLarge) == 0 {
		fmt.Fprintln(output, "No oversized HTML pages were found.")
		return
	}

	for problemIndex, problem := range result.HTMLTooLarge {
		fmt.Fprintln(output)
		fmt.Fprintf(output, "HTML_TOO_LARGE %d\n", problemIndex+1)
		writeProblem(output, problem)
	}
}

func writeProblem(output io.Writer, problem crawler.Problem) {
	fmt.Fprintf(output, "URL:    %s\n", problem.URL)
	fmt.Fprintf(output, "Result: %s\n", problem.Kind)
	if problem.Status != "" {
		fmt.Fprintf(output, "Status: %s\n", problem.Status)
	}
	if problem.Error != "" {
		fmt.Fprintf(output, "Error:  %s\n", problem.Error)
	}
	fmt.Fprintln(output, "Found on:")
	writeSources(output, problem.Sources)
}

func formatElapsed(elapsed time.Duration) time.Duration {
	if elapsed < time.Millisecond {
		return elapsed.Round(time.Microsecond)
	}
	return elapsed.Round(time.Millisecond)
}

func parseHTTPStatusCode(status string) (int, bool) {
	fields := strings.Fields(status)
	if len(fields) == 0 {
		return 0, false
	}

	statusCode, err := strconv.Atoi(fields[0])
	if err != nil || statusCode < 100 || statusCode > 999 {
		return 0, false
	}
	return statusCode, true
}

func writeHTTPStatusCounts(
	output io.Writer,
	counts map[int]int,
	minimum int,
	maximum int,
) {
	statusCodes := make([]int, 0)
	for statusCode := range counts {
		if statusCode >= minimum && statusCode <= maximum {
			statusCodes = append(statusCodes, statusCode)
		}
	}
	sort.Ints(statusCodes)
	for _, statusCode := range statusCodes {
		fmt.Fprintf(output, "HTTP %d:           %d\n", statusCode, counts[statusCode])
	}
}

func writeSources(output io.Writer, sources []string) {
	if len(sources) == 0 {
		fmt.Fprintln(output, "- [start URL]")
		return
	}
	ordered := append([]string(nil), sources...)
	sort.Strings(ordered)
	for _, source := range ordered {
		fmt.Fprintf(output, "- %s\n", source)
	}
}
