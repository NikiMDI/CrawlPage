package crawler_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

// Compare the crawler with a small reference BFS on generated cyclic graphs.
// Queue capacity and worker count must not change which pages are reachable.
func TestGeneratedGraphsDoNotLosePagesWhenQueueFills(t *testing.T) {
	const (
		pageCount = 45
		maxDepth  = 4
	)
	settings := []struct {
		maxQueue    int
		concurrency int
	}{
		{maxQueue: 1, concurrency: 1},
		{maxQueue: 2, concurrency: 1},
		{maxQueue: 1, concurrency: 4},
		{maxQueue: 3, concurrency: 4},
		{maxQueue: 8, concurrency: 2},
	}

	for _, seed := range []int64{1, 7, 23, 99, 2026} {
		graph := makeGeneratedGraph(pageCount, seed)
		wantDepth := reachableDepths(graph, maxDepth)
		for _, setting := range settings {
			t.Run(fmt.Sprintf("seed=%d/queue=%d/workers=%d", seed, setting.maxQueue, setting.concurrency), func(t *testing.T) {
				var requests requestRecorder
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.add(r.URL.Path)
					if !strings.HasPrefix(r.URL.Path, "/p/") {
						http.NotFound(w, r)
						return
					}
					page, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/p/"))
					if err != nil || page < 0 || page >= len(graph) {
						http.NotFound(w, r)
						return
					}
					links := make([]string, 0, len(graph[page])+2)
					for index, child := range graph[page] {
						href := fmt.Sprintf("/p/%d", child)
						if index%2 == 0 {
							href = fmt.Sprintf("../p/%d", child)
						}
						links = append(links, fmt.Sprintf(`<a href="%s">Next</a>`, href))
					}
					links = append(links,
						fmt.Sprintf(`<a href="/p/%d#again">Duplicate</a>`, graph[page][0]),
						`<a href="https://example.org/outside">External</a>`,
					)
					writeTestHTML(w, links...)
				}))
				defer server.Close()

				inspector, err := crawler.New(crawler.Config{
					StartURL:       server.URL + "/p/0",
					RequestTimeout: 2 * time.Second,
					MaxDepth:       maxDepth,
					MaxPages:       pageCount + 10,
					MaxRedirects:   10,
					Concurrency:    setting.concurrency,
					MaxQueue:       setting.maxQueue,
				})
				if err != nil {
					t.Fatalf("crawler.New: %v", err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := inspector.Crawl(ctx)
				if err != nil {
					t.Fatalf("Crawl: %v", err)
				}
				if result.PagesChecked != len(wantDepth) || len(result.Pages) != len(wantDepth) {
					t.Fatalf("checked/results = %d/%d, want %d reachable pages", result.PagesChecked, len(result.Pages), len(wantDepth))
				}
				if result.PeakQueueSize > setting.maxQueue {
					t.Fatalf("peak queue = %d, maximum = %d", result.PeakQueueSize, setting.maxQueue)
				}
				if result.MaxPagesReached {
					t.Fatal("the page budget was not exhausted")
				}

				seen := make(map[int]int)
				for _, page := range result.Pages {
					parsed, err := url.Parse(page.URL)
					if err != nil {
						t.Fatalf("parse result URL %q: %v", page.URL, err)
					}
					index, err := strconv.Atoi(strings.TrimPrefix(parsed.Path, "/p/"))
					if err != nil {
						t.Fatalf("unexpected page URL %q", page.URL)
					}
					want, expected := wantDepth[index]
					if !expected || !page.HTMLParsed || page.ResultKind != crawler.ResultSuccess {
						t.Fatalf("unexpected checked page %+v", page)
					}
					if page.Depth != want {
						t.Errorf("page %d depth = %d, want shortest depth %d", index, page.Depth, want)
					}
					seen[index]++
				}
				for index, want := range wantDepth {
					if seen[index] != 1 {
						t.Errorf("page %d checked %d times, want once", index, seen[index])
					}
					if got := result.DepthByURL[fmt.Sprintf("%s/p/%d", server.URL, index)]; got != want {
						t.Errorf("page %d recorded depth = %d, want %d", index, got, want)
					}
				}

				requestCounts := make(map[string]int)
				for _, path := range requests.snapshot() {
					requestCounts[path]++
				}
				if len(requestCounts) != len(wantDepth) {
					t.Errorf("requested %d unique pages, want %d", len(requestCounts), len(wantDepth))
				}
				for path, count := range requestCounts {
					if count != 1 {
						t.Errorf("%s requested %d times, want once", path, count)
					}
				}
			})
		}
	}
}

func makeGeneratedGraph(pageCount int, seed int64) [][]int {
	random := rand.New(rand.NewSource(seed))
	graph := make([][]int, pageCount)
	for page := range graph {
		graph[page] = []int{(page + 1) % pageCount}
		for range 4 {
			graph[page] = append(graph[page], random.Intn(pageCount))
		}
		graph[page] = append(graph[page], page)
	}
	return graph
}

func reachableDepths(graph [][]int, maxDepth int) map[int]int {
	depths := map[int]int{0: 0}
	queue := []int{0}
	for len(queue) > 0 {
		page := queue[0]
		queue = queue[1:]
		if depths[page] >= maxDepth {
			continue
		}
		for _, child := range graph[page] {
			if _, known := depths[child]; known {
				continue
			}
			depths[child] = depths[page] + 1
			queue = append(queue, child)
		}
	}
	return depths
}
