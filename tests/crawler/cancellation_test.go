package crawler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestCancellationStopsConcurrentRequestsAndQueuedJobs(t *testing.T) {
	const concurrency = 4

	allStarted := make(chan struct{})
	allStopped := make(chan struct{})
	var started atomic.Int32
	var stopped atomic.Int32
	var active atomic.Int32
	var neverRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			links := make([]string, 0, concurrency+1)
			for index := 1; index <= concurrency; index++ {
				links = append(links, fmt.Sprintf(`<a href="/block-%d">Block</a>`, index))
			}
			links = append(links, `<a href="/never">Never</a>`)
			writeTestHTML(w, links...)
			return
		}
		if r.URL.Path == "/never" {
			neverRequests.Add(1)
			writeTestHTML(w)
			return
		}

		active.Add(1)
		if started.Add(1) == concurrency {
			close(allStarted)
		}
		<-r.Context().Done()
		active.Add(-1)
		if stopped.Add(1) == concurrency {
			close(allStopped)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspector := newConcurrentInspector(t, server.URL+"/", 1, 20, concurrency)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(ctx)
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-allStarted:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("concurrent requests did not all start")
	}

	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("Crawl did not return after parent cancellation")
	}
	select {
	case <-allStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP handlers remained active after cancellation")
	}

	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("Crawl error = %v, want context canceled", outcome.err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active requests after Crawl = %d, want 0", got)
	}
	if got := neverRequests.Load(); got != 0 {
		t.Fatalf("queued page was requested after cancellation %d times", got)
	}
	if outcome.result.PagesChecked != 1 {
		t.Fatalf(
			"pages checked = %d, want only the root whose response headers were received",
			outcome.result.PagesChecked,
		)
	}
	if len(outcome.result.Problems) != 0 {
		t.Fatalf("cancellation created broken-link problems: %+v", outcome.result.Problems)
	}
}

func TestCancellationUnblocksSharedFetchWaiter(t *testing.T) {
	sharedStarted := make(chan struct{})
	redirectDelivered := make(chan struct{})
	var sharedOnce sync.Once
	var redirectOnce sync.Once
	var sharedRequests atomic.Int32
	var neverRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeTestHTML(
				w,
				`<a href="/shared">Shared</a>`,
				`<a href="/old">Old</a>`,
				`<a href="/never">Never</a>`,
			)
		case "/shared":
			sharedRequests.Add(1)
			sharedOnce.Do(func() { close(sharedStarted) })
			<-r.Context().Done()
		case "/old":
			select {
			case <-sharedStarted:
				w.Header().Set("Location", "/shared")
				w.WriteHeader(http.StatusFound)
				w.(http.Flusher).Flush()
				redirectOnce.Do(func() { close(redirectDelivered) })
			case <-r.Context().Done():
			}
		case "/never":
			neverRequests.Add(1)
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspector := newConcurrentInspector(t, server.URL+"/", 1, 10, 2)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(ctx)
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-redirectDelivered:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("redirect to the in-flight shared target was not delivered")
	}

	// Give the redirect worker a bounded window to enter session.fetch and wait
	// for the already in-flight /shared entry. A broken cache would issue a
	// second physical request during this window.
	select {
	case <-time.After(200 * time.Millisecond):
		cancel()
	case <-ctx.Done():
	}

	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("singleflight waiter did not stop after cancellation")
	}

	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("Crawl error = %v, want context canceled", outcome.err)
	}
	if got := sharedRequests.Load(); got != 1 {
		t.Fatalf("shared target requests = %d, want exactly 1", got)
	}
	if got := neverRequests.Load(); got != 0 {
		t.Fatalf("queued page was requested after cancellation %d times", got)
	}
	if len(outcome.result.Problems) != 0 {
		t.Fatalf("cancellation created broken-link problems: %+v", outcome.result.Problems)
	}
}

func TestCancellationDuringRedirectTargetBodyDoesNotAppendPartialPage(t *testing.T) {
	bodyStarted := make(chan struct{})
	bodyStopped := make(chan struct{})
	var bodyStartOnce sync.Once
	var bodyStopOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/body")
			w.WriteHeader(http.StatusFound)
		case "/body":
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			bodyStartOnce.Do(func() { close(bodyStarted) })
			<-r.Context().Done()
			bodyStopOnce.Do(func() { close(bodyStopped) })
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspector := newConcurrentInspector(t, server.URL+"/redirect", 0, 10, 2)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(ctx)
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-bodyStarted:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("redirect target body did not start")
	}

	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("Crawl did not stop while reading a response body")
	}
	select {
	case <-bodyStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("response body handler did not observe cancellation")
	}

	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("Crawl error = %v, want context canceled", outcome.err)
	}
	if len(outcome.result.Pages) != 0 {
		t.Fatalf("cancelled redirect produced partial pages: %+v", outcome.result.Pages)
	}
	if len(outcome.result.Problems) != 0 {
		t.Fatalf("cancelled redirect produced problems: %+v", outcome.result.Problems)
	}
	if outcome.result.PagesChecked < 1 || outcome.result.PagesChecked > 2 {
		t.Fatalf(
			"pages checked = %d, want one or two admitted responses depending on the cancellation race",
			outcome.result.PagesChecked,
		)
	}
}
