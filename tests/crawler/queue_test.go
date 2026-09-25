package crawler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestCrawlBoundsPendingQueueWithoutLosingLinks(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			writeTestHTML(
				w,
				`<a href="/a">A</a>`,
				`<a href="/b">B</a>`,
				`<a href="/c">C</a>`,
			)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	result := crawlWithQueueLimit(t, server.URL+"/", 2)

	if got, want := requests.snapshot(), []string{"/", "/a", "/b", "/c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want every discovered URL %v", got, want)
	}
	if result.MaxQueue != 2 || result.PeakQueueSize != 2 {
		t.Fatalf("queue limit/peak = %d/%d, want 2/2", result.MaxQueue, result.PeakQueueSize)
	}
	if !result.QueueLimitReached {
		t.Fatal("QueueLimitReached = false, want true when links wait outside ready slots")
	}
	if result.PagesChecked != 4 {
		t.Fatalf("PagesChecked = %d, want all four HTML pages", result.PagesChecked)
	}
	if result.MaxPagesReached {
		t.Fatal("queue pressure must not be reported as a max-pages overflow")
	}
}

func TestSmallQueuePreservesDFSOrder(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(
				w,
				`<a href="/a">A</a>`,
				`<a href="/b">B</a>`,
				`<a href="/c">C</a>`,
			)
		case "/a":
			writeTestHTML(w, `<a href="/a-1">A1</a>`)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	result := crawlWithQueueLimit(t, server.URL+"/", 2)

	wantOrder := []string{"/", "/a", "/a-1", "/b", "/c"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want DFS order %v", got, wantOrder)
	}
	if result.PeakQueueSize > result.MaxQueue {
		t.Fatalf("peak queue size = %d, exceeds configured maximum %d", result.PeakQueueSize, result.MaxQueue)
	}
	if !result.QueueLimitReached {
		t.Fatal("QueueLimitReached = false, want true when DFS needs to defer a sibling")
	}
	if result.PagesChecked != len(wantOrder) {
		t.Fatalf("PagesChecked = %d, want %d", result.PagesChecked, len(wantOrder))
	}
}

func TestPendingSharedURLKeepsDFSOrderAcrossQueueSizes(t *testing.T) {
	for _, maxQueue := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("queue=%d", maxQueue), func(t *testing.T) {
			var requests requestRecorder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.add(r.URL.Path)
				switch r.URL.Path {
				case "/":
					writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`)
				case "/a":
					writeTestHTML(w, `<a href="/b">B again</a>`, `<a href="/c">C</a>`)
				default:
					writeTestHTML(w)
				}
			}))
			defer server.Close()

			inspector, err := crawler.New(crawler.Config{
				StartURL:       server.URL + "/",
				RequestTimeout: time.Second,
				MaxDepth:       2,
				MaxPages:       3,
				MaxRedirects:   10,
				Concurrency:    1,
				MaxQueue:       maxQueue,
			})
			if err != nil {
				t.Fatalf("crawler.New: %v", err)
			}
			result, err := inspector.Crawl(context.Background())
			if err != nil {
				t.Fatalf("Crawl: %v", err)
			}
			if got, want := requests.snapshot(), []string{"/", "/a", "/b", "/c"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("requests = %v, want DFS order %v", got, want)
			}
			if result.PagesChecked != 3 {
				t.Fatalf("pages checked = %d, want 3", result.PagesChecked)
			}
			if _, found := result.SourcesByURL[server.URL+"/b"]; !found {
				t.Fatal("shared URL B should be discovered")
			}
			findPage(t, result, server.URL+"/b")
			for _, page := range result.Pages {
				if page.URL == server.URL+"/c" {
					t.Fatal("C was admitted before B despite DFS and a three-page budget")
				}
			}
			if result.PeakQueueSize > maxQueue {
				t.Fatalf("peak queue = %d, maximum = %d", result.PeakQueueSize, maxQueue)
			}
		})
	}
}

func TestQueueExactlyAtLimitIsNotReportedAsOverflow(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	result := crawlWithQueueLimit(t, server.URL+"/", 2)

	if got, want := requests.snapshot(), []string{"/", "/a", "/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if result.PeakQueueSize != 2 {
		t.Fatalf("peak queue size = %d, want 2", result.PeakQueueSize)
	}
	if result.QueueLimitReached {
		t.Fatal("QueueLimitReached = true, want false at the exact ready-slot limit")
	}
}

func TestDuplicateLinksUseOneQueueSlot(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			writeTestHTML(
				w,
				`<a href="/a">A first</a>`,
				`<a href="/a#section">A duplicate</a>`,
				`<a href="/b">B</a>`,
			)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	result := crawlWithQueueLimit(t, server.URL+"/", 2)

	if got, want := requests.snapshot(), []string{"/", "/a", "/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want each normalized URL once: %v", got, want)
	}
	if result.QueueLimitReached {
		t.Fatal("duplicate link consumed a ready slot")
	}
	if result.PeakQueueSize != 2 {
		t.Fatalf("peak queue size = %d, want 2 unique pending URLs", result.PeakQueueSize)
	}
}

func TestQueuePressureRequiresAnotherRunnableURL(t *testing.T) {
	for _, test := range []struct {
		name         string
		secondLink   string
		wantPressure bool
		wantRequests []string
	}{
		{
			name:         "duplicate fragment",
			secondLink:   `<a href="/a#section">A duplicate</a>`,
			wantRequests: []string{"/", "/a"},
		},
		{
			name:         "external URL",
			secondLink:   `<a href="https://example.org/elsewhere">External</a>`,
			wantRequests: []string{"/", "/a"},
		},
		{
			name:         "another internal URL",
			secondLink:   `<a href="/b">B</a>`,
			wantPressure: true,
			wantRequests: []string{"/", "/a", "/b"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests requestRecorder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.add(r.URL.Path)
				if r.URL.Path == "/" {
					writeTestHTML(w, `<a href="/a">A</a>`, test.secondLink)
					return
				}
				writeTestHTML(w)
			}))
			defer server.Close()

			result := crawlWithQueueLimit(t, server.URL+"/", 1)
			if got := requests.snapshot(); !reflect.DeepEqual(got, test.wantRequests) {
				t.Fatalf("requests = %v, want %v", got, test.wantRequests)
			}
			if result.QueueLimitReached != test.wantPressure {
				t.Fatalf("QueueLimitReached = %t, want %t", result.QueueLimitReached, test.wantPressure)
			}
			if result.PeakQueueSize != 1 {
				t.Fatalf("peak queue size = %d, want 1", result.PeakQueueSize)
			}
		})
	}
}

func TestQueueBoundHoldsWithMultipleWorkers(t *testing.T) {
	const (
		children    = 12
		maxQueue    = 3
		concurrency = 4
	)
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			links := make([]string, 0, children)
			for index := 1; index <= children; index++ {
				links = append(links, fmt.Sprintf(`<a href="/page-%d">Page</a>`, index))
			}
			writeTestHTML(w, links...)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/",
		RequestTimeout: time.Second,
		MaxDepth:       1,
		MaxPages:       100,
		MaxRedirects:   10,
		Concurrency:    concurrency,
		MaxQueue:       maxQueue,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if result.PeakQueueSize != maxQueue || result.PeakQueueSize > result.MaxQueue {
		t.Fatalf("peak/limit = %d/%d, want %d/%d", result.PeakQueueSize, result.MaxQueue, maxQueue, maxQueue)
	}
	seen := make(map[string]int)
	for _, path := range requests.snapshot() {
		seen[path]++
	}
	if got, want := len(seen), children+1; got != want {
		t.Fatalf("unique requests = %d, want all %d discovered HTML pages; requests = %v", got, want, requests.snapshot())
	}
	for path, count := range seen {
		if count != 1 {
			t.Errorf("%s requested %d times, want once", path, count)
		}
	}
	if result.PagesChecked != children+1 {
		t.Fatalf("PagesChecked = %d, want %d", result.PagesChecked, children+1)
	}
	if !result.QueueLimitReached {
		t.Fatal("QueueLimitReached = false, want true for a wide page")
	}
}

func TestOneReadySlotChecksEveryLinkOnWidePage(t *testing.T) {
	const children = 128
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			links := make([]string, 0, children)
			for index := 1; index <= children; index++ {
				links = append(links, fmt.Sprintf(`<a href="/page-%d">Page</a>`, index))
			}
			writeTestHTML(w, links...)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	result := crawlWithQueueLimit(t, server.URL+"/", 1)
	wantOrder := make([]string, 0, children+1)
	wantOrder = append(wantOrder, "/")
	for index := 1; index <= children; index++ {
		wantOrder = append(wantOrder, fmt.Sprintf("/page-%d", index))
	}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("requests = %v, want all %d pages in DFS order", got, children+1)
	}
	if result.PagesChecked != children+1 || len(result.Pages) != children+1 {
		t.Fatalf("checked/results = %d/%d, want %d/%d", result.PagesChecked, len(result.Pages), children+1, children+1)
	}
	if result.PeakQueueSize != 1 || !result.QueueLimitReached {
		t.Fatalf("peak/pressure = %d/%t, want 1/true", result.PeakQueueSize, result.QueueLimitReached)
	}
}

func TestOneReadySlotAllowsLaterShallowerPath(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/long">Long</a>`, `<a href="/short">Short</a>`)
		case "/long":
			writeTestHTML(w, `<a href="/middle">Middle</a>`)
		case "/middle":
			writeTestHTML(w, `<a href="/visited">Visited</a>`)
		case "/visited":
			writeTestHTML(w, `<a href="/child">Child</a>`)
		case "/short":
			writeTestHTML(w, `<a href="/visited">Visited by shorter path</a>`)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/",
		RequestTimeout: time.Second,
		MaxDepth:       3,
		MaxPages:       100,
		MaxRedirects:   10,
		Concurrency:    1,
		MaxQueue:       1,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if got, want := requests.snapshot(), []string{"/", "/long", "/middle", "/visited", "/short", "/child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want later shallow path to open /child: %v", got, want)
	}
	if got := result.DepthByURL[server.URL+"/visited"]; got != 2 {
		t.Fatalf("visited depth = %d, want 2", got)
	}
	if got := result.DepthByURL[server.URL+"/child"]; got != 3 {
		t.Fatalf("child depth = %d, want 3", got)
	}
	if result.PeakQueueSize > 1 {
		t.Fatalf("peak queue = %d, want at most 1", result.PeakQueueSize)
	}
}

func TestCancellationStopsFullQueueWithoutDispatchingDeferredLinks(t *testing.T) {
	var requests requestRecorder
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`, `<a href="/c">C</a>`)
			return
		}
		if r.URL.Path == "/a" {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/",
		RequestTimeout: 5 * time.Second,
		MaxDepth:       1,
		MaxPages:       100,
		MaxRedirects:   10,
		Concurrency:    1,
		MaxQueue:       1,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, crawlErr := inspector.Crawl(ctx)
		finished <- crawlErr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first child was not dispatched")
	}
	cancel()
	select {
	case crawlErr := <-finished:
		if !errors.Is(crawlErr, context.Canceled) {
			t.Fatalf("Crawl error = %v, want context.Canceled", crawlErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not stop after cancellation with a full queue")
	}
	if got, want := requests.snapshot(), []string{"/", "/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want only dispatched jobs %v", got, want)
	}
}

func crawlWithQueueLimit(t *testing.T, startURL string, maxQueue int) crawler.CrawlResult {
	t.Helper()
	inspector, err := crawler.New(crawler.Config{
		StartURL:       startURL,
		RequestTimeout: time.Second,
		MaxDepth:       5,
		MaxPages:       1000,
		MaxRedirects:   10,
		Concurrency:    1,
		MaxQueue:       maxQueue,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}

	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	return result
}
