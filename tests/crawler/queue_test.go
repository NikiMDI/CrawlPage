package crawler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestCrawlBoundsPendingQueueAndReportsOverflow(t *testing.T) {
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

	if got, want := requests.snapshot(), []string{"/", "/a", "/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want only the two URLs admitted to the queue %v", got, want)
	}
	if result.MaxQueue != 2 || result.PeakQueueSize != 2 {
		t.Fatalf("queue limit/peak = %d/%d, want 2/2", result.MaxQueue, result.PeakQueueSize)
	}
	if !result.QueueLimitReached || result.QueueLinksSkipped != 1 {
		t.Fatalf(
			"queue reached/skipped = %t/%d, want true/1",
			result.QueueLimitReached,
			result.QueueLinksSkipped,
		)
	}
	if result.MaxPagesReached {
		t.Fatal("queue overflow must not be reported as a max-pages overflow")
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

	wantOrder := []string{"/", "/a", "/a-1", "/b"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want DFS order %v", got, wantOrder)
	}
	if result.PeakQueueSize > result.MaxQueue {
		t.Fatalf("peak queue size = %d, exceeds configured maximum %d", result.PeakQueueSize, result.MaxQueue)
	}
	if !result.QueueLimitReached || result.QueueLinksSkipped != 1 {
		t.Fatalf(
			"queue reached/skipped = %t/%d, want true/1 for /c",
			result.QueueLimitReached,
			result.QueueLinksSkipped,
		)
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
	if result.QueueLimitReached || result.QueueLinksSkipped != 0 {
		t.Fatalf(
			"queue reached/skipped = %t/%d, want false/0",
			result.QueueLimitReached,
			result.QueueLinksSkipped,
		)
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
	if result.QueueLimitReached || result.QueueLinksSkipped != 0 {
		t.Fatalf(
			"duplicate consumed a queue slot: reached/skipped = %t/%d",
			result.QueueLimitReached,
			result.QueueLinksSkipped,
		)
	}
	if result.PeakQueueSize != 2 {
		t.Fatalf("peak queue size = %d, want 2 unique pending URLs", result.PeakQueueSize)
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
	if got, want := len(requests.snapshot()), 1+maxQueue; got != want {
		t.Fatalf("requests = %d, want root plus %d admitted jobs", got, maxQueue)
	}
	if !result.QueueLimitReached || result.QueueLinksSkipped != children-maxQueue {
		t.Fatalf(
			"queue reached/skipped = %t/%d, want true/%d",
			result.QueueLimitReached,
			result.QueueLinksSkipped,
			children-maxQueue,
		)
	}
}

func crawlWithQueueLimit(t *testing.T, startURL string, maxQueue int) crawler.CrawlResult {
	t.Helper()
	inspector, err := crawler.New(crawler.Config{
		StartURL:       startURL,
		RequestTimeout: time.Second,
		MaxDepth:       5,
		MaxPages:       100,
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
