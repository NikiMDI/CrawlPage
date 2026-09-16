package crawler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestConcurrentCrawlHonorsLimitAndActuallyOverlaps(t *testing.T) {
	const (
		concurrency = 3
		children    = 6
	)

	var active atomic.Int32
	var peak atomic.Int32
	started := make(chan string, children)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			links := make([]string, 0, children)
			for index := 1; index <= children; index++ {
				links = append(links, fmt.Sprintf(`<a href="/job-%d">Job</a>`, index))
			}
			writeTestHTML(w, links...)
			return
		}

		current := active.Add(1)
		updateAtomicMaximum(&peak, current)
		defer active.Add(-1)
		started <- r.URL.Path
		select {
		case <-release:
			writeTestHTML(w)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer releaseAll()

	inspector := newConcurrentInspector(t, server.URL+"/", 1, 20, concurrency)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(context.Background())
		finished <- crawlOutcome{result: result, err: err}
	}()

	for index := 0; index < concurrency; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("three requests did not overlap")
		}
	}
	select {
	case path := <-started:
		t.Fatalf("request %s started above concurrency limit", path)
	case <-time.After(200 * time.Millisecond):
	}

	releaseAll()
	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not finish after releasing requests")
	}
	if outcome.err != nil {
		t.Fatalf("Crawl: %v", outcome.err)
	}
	if got := peak.Load(); got != concurrency {
		t.Fatalf("peak concurrent requests = %d, want %d", got, concurrency)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active requests after Crawl = %d, want 0", got)
	}
	if outcome.result.PagesChecked != children+1 {
		t.Fatalf("pages checked = %d, want %d", outcome.result.PagesChecked, children+1)
	}
	if outcome.result.Concurrency != concurrency {
		t.Fatalf("reported concurrency = %d, want %d", outcome.result.Concurrency, concurrency)
	}
}

func TestConcurrencyOnePreservesStrictDFS(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`)
		case "/a":
			writeTestHTML(w, `<a href="/a-1">A1</a>`)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	inspector := newConcurrentInspector(t, server.URL+"/", 2, 20, 1)
	_, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	want := []string{"/", "/a", "/a-1", "/b"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("request order = %v, want strict DFS %v", got, want)
	}
}

func TestConcurrentDirectAndRedirectTargetFetchedOnce(t *testing.T) {
	var directRequests atomic.Int32
	directStarted := make(chan struct{})
	redirectDelivered := make(chan struct{})
	duplicateDirect := make(chan struct{}, 1)
	releaseDirect := make(chan struct{})
	var directOnce sync.Once
	var redirectOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDirect) }) }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/direct">Direct</a>`, `<a href="/redirect">Redirect</a>`)
		case "/direct":
			if directRequests.Add(1) == 1 {
				directOnce.Do(func() { close(directStarted) })
			} else {
				select {
				case duplicateDirect <- struct{}{}:
				default:
				}
			}
			select {
			case <-releaseDirect:
				writeTestHTML(w)
			case <-r.Context().Done():
			}
		case "/redirect":
			select {
			case <-directStarted:
				w.Header().Set("Location", "/direct")
				w.WriteHeader(http.StatusFound)
				w.(http.Flusher).Flush()
				redirectOnce.Do(func() { close(redirectDelivered) })
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer release()

	inspector := newConcurrentInspector(t, server.URL+"/", 1, 10, 2)
	finished := make(chan error, 1)
	var result crawler.CrawlResult
	go func() {
		var err error
		result, err = inspector.Crawl(context.Background())
		finished <- err
	}()

	select {
	case <-redirectDelivered:
	case <-time.After(time.Second):
		t.Fatal("direct and redirect requests did not overlap")
	}
	select {
	case <-duplicateDirect:
		t.Fatal("redirect target was requested again while its first request was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	var crawlErr error
	select {
	case crawlErr = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not finish after releasing the shared target")
	}
	if crawlErr != nil {
		t.Fatalf("Crawl: %v", crawlErr)
	}

	if got := directRequests.Load(); got != 1 {
		t.Fatalf("direct target requests = %d, want 1", got)
	}
	if result.PagesChecked != 3 {
		t.Fatalf("pages checked = %d, want 3", result.PagesChecked)
	}
	redirectPage := findPage(t, result, server.URL+"/redirect")
	if redirectPage.ResultKind != crawler.ResultSuccess ||
		redirectPage.FinalURL != server.URL+"/direct" ||
		len(redirectPage.RedirectChain) != 1 {
		t.Fatalf("redirect page = %+v", redirectPage)
	}
}

func TestConcurrentRedirectCycleDoesNotDeadlock(t *testing.T) {
	bothArrived := make(chan struct{})
	release := make(chan struct{})
	var arrivals atomic.Int32
	var arrivalOnce sync.Once
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	var requests requestRecorder

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`)
		case "/a", "/b":
			if arrivals.Add(1) >= 2 {
				arrivalOnce.Do(func() { close(bothArrived) })
			}
			select {
			case <-release:
				target := "/a"
				if r.URL.Path == "/a" {
					target = "/b"
				}
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer releaseAll()

	inspector := newConcurrentInspector(t, server.URL+"/", 1, 10, 2)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(context.Background())
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-bothArrived:
	case <-time.After(time.Second):
		t.Fatal("redirect endpoints did not start concurrently")
	}
	releaseAll()

	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("crawl deadlocked on intersecting redirect chains")
	}
	if outcome.err != nil {
		t.Fatalf("Crawl: %v", outcome.err)
	}
	if outcome.result.PagesChecked != 3 {
		t.Fatalf("pages checked = %d, want 3", outcome.result.PagesChecked)
	}
	if countPath(requests.snapshot(), "/a") != 1 || countPath(requests.snapshot(), "/b") != 1 {
		t.Fatalf("redirect endpoints were requested more than once: %v", requests.snapshot())
	}
	for _, path := range []string{"/a", "/b"} {
		page := findPage(t, outcome.result, server.URL+path)
		if page.ResultKind != crawler.ResultRedirectError || !page.RedirectCycleDetected {
			t.Errorf("page %s = %+v, want redirect cycle", path, page)
		}
	}
}

func TestConcurrentBranchesScheduleSharedURLOnce(t *testing.T) {
	var branchArrivals atomic.Int32
	var barrierOnce sync.Once
	var sharedRequests atomic.Int32
	bothBranchesArrived := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/left">Left</a>`, `<a href="/right">Right</a>`)
		case "/left", "/right":
			if branchArrivals.Add(1) >= 2 {
				barrierOnce.Do(func() { close(bothBranchesArrived) })
			}
			select {
			case <-bothBranchesArrived:
				writeTestHTML(w, `<a href="/shared">Shared</a>`)
			case <-r.Context().Done():
			}
		case "/shared":
			sharedRequests.Add(1)
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newConcurrentInspector(t, server.URL+"/", 2, 10, 2)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if got := sharedRequests.Load(); got != 1 {
		t.Fatalf("shared URL requests = %d, want 1", got)
	}
	if result.PagesChecked != 4 {
		t.Fatalf("pages checked = %d, want 4", result.PagesChecked)
	}
	shared := findPage(t, result, server.URL+"/shared")
	if shared.Depth != 2 {
		t.Fatalf("shared depth = %d, want 2", shared.Depth)
	}
	if got := len(result.SourcesByURL[server.URL+"/shared"]); got != 2 {
		t.Fatalf("shared source count = %d, want 2", got)
	}
}

func TestConcurrentDepthRelaxationReexpandsWithoutRefetch(t *testing.T) {
	targetStarted := make(chan struct{})
	releaseShort := make(chan struct{})
	markerRequested := make(chan struct{})
	var targetOnce sync.Once
	var markerOnce sync.Once
	var releaseOnce sync.Once
	releaseShortBranch := func() { releaseOnce.Do(func() { close(releaseShort) }) }
	var targetRequests atomic.Int32
	var leafRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeTestHTML(
				w,
				`<a href="/long-1">Long path</a>`,
				`<a href="/short-parent">Short path</a>`,
			)
		case "/long-1":
			writeTestHTML(w, `<a href="/long-2">Long 2</a>`)
		case "/long-2":
			writeTestHTML(w, `<a href="/target">Target at depth 3</a>`)
		case "/short-parent":
			select {
			case <-releaseShort:
				writeTestHTML(
					w,
					`<a href="/target">Target at depth 2</a>`,
					`<a href="/marker">Marker</a>`,
				)
			case <-r.Context().Done():
			}
		case "/target":
			targetRequests.Add(1)
			targetOnce.Do(func() { close(targetStarted) })
			select {
			case <-markerRequested:
				writeTestHTML(w, `<a href="/leaf">Leaf</a>`)
			case <-r.Context().Done():
			}
		case "/marker":
			markerOnce.Do(func() { close(markerRequested) })
			writeTestHTML(w)
		case "/leaf":
			leafRequests.Add(1)
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	crawlContext, cancelCrawl := context.WithCancel(context.Background())
	defer cancelCrawl()
	defer releaseShortBranch()

	inspector := newConcurrentInspector(t, server.URL+"/", 3, 20, 2)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(crawlContext)
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-targetStarted:
	case <-time.After(time.Second):
		t.Fatal("target was not started through the long path")
	}
	releaseShortBranch()

	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not finish after depth relaxation")
	}
	if outcome.err != nil {
		t.Fatalf("Crawl: %v", outcome.err)
	}

	targetURL := server.URL + "/target"
	target := findPage(t, outcome.result, targetURL)
	if target.Depth != 2 || target.FetchDepth != 3 {
		t.Fatalf(
			"target depth/fetch depth = %d/%d, want 2/3",
			target.Depth,
			target.FetchDepth,
		)
	}
	if got := outcome.result.DepthByURL[targetURL]; got != 2 {
		t.Fatalf("DepthByURL[target] = %d, want 2", got)
	}
	if got := targetRequests.Load(); got != 1 {
		t.Fatalf("target requests = %d, want 1", got)
	}
	if got := leafRequests.Load(); got != 1 {
		t.Fatalf("leaf requests = %d, want 1 after re-expansion", got)
	}
	leaf := findPage(t, outcome.result, server.URL+"/leaf")
	if leaf.Depth != 3 {
		t.Fatalf("leaf depth = %d, want 3", leaf.Depth)
	}
}

func TestConcurrentCrawlNeverExceedsMaxPages(t *testing.T) {
	const children = 16
	var totalRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests.Add(1)
		if r.URL.Path == "/" {
			links := make([]string, 0, children)
			for index := 1; index <= children; index++ {
				links = append(links, fmt.Sprintf(`<a href="/page-%d">Page</a>`, index))
			}
			writeTestHTML(w, links...)
			return
		}
		time.Sleep(10 * time.Millisecond)
		writeTestHTML(w)
	}))
	defer server.Close()

	inspector := newConcurrentInspector(t, server.URL+"/", 1, 5, 8)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if got := totalRequests.Load(); got != 5 {
		t.Fatalf("HTTP requests = %d, want exactly max-pages 5", got)
	}
	if result.PagesChecked != 5 || !result.MaxPagesReached {
		t.Fatalf(
			"pages checked/max reached = %d / %t, want 5 / true",
			result.PagesChecked,
			result.MaxPagesReached,
		)
	}
}

func TestConcurrentRedirectsShareMaxPagesBudget(t *testing.T) {
	bothArrived := make(chan struct{})
	release := make(chan struct{})
	var arrivals atomic.Int32
	var arrivalOnce sync.Once
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	var totalRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests.Add(1)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/r1">R1</a>`, `<a href="/r2">R2</a>`)
		case "/r1", "/r2":
			if arrivals.Add(1) >= 2 {
				arrivalOnce.Do(func() { close(bothArrived) })
			}
			select {
			case <-release:
				w.Header().Set("Location", "/final-"+r.URL.Path[2:])
				w.WriteHeader(http.StatusFound)
			case <-r.Context().Done():
			}
		case "/final-1", "/final-2":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer releaseAll()

	inspector := newConcurrentInspector(t, server.URL+"/", 1, 4, 2)
	type crawlOutcome struct {
		result crawler.CrawlResult
		err    error
	}
	finished := make(chan crawlOutcome, 1)
	go func() {
		result, err := inspector.Crawl(context.Background())
		finished <- crawlOutcome{result: result, err: err}
	}()

	select {
	case <-bothArrived:
	case <-time.After(time.Second):
		t.Fatal("redirect sources did not start concurrently")
	}
	releaseAll()
	var outcome crawlOutcome
	select {
	case outcome = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not finish after releasing redirect sources")
	}
	if outcome.err != nil {
		t.Fatalf("Crawl: %v", outcome.err)
	}
	if totalRequests.Load() != 4 || outcome.result.PagesChecked != 4 || !outcome.result.MaxPagesReached {
		t.Fatalf(
			"requests/pages/max reached = %d / %d / %t, want 4 / 4 / true",
			totalRequests.Load(),
			outcome.result.PagesChecked,
			outcome.result.MaxPagesReached,
		)
	}

	successes := 0
	pageLimits := 0
	for _, path := range []string{"/r1", "/r2"} {
		page := findPage(t, outcome.result, server.URL+path)
		switch page.ResultKind {
		case crawler.ResultSuccess:
			successes++
		case crawler.ResultPageLimit:
			pageLimits++
			if !page.StoppedByPageLimit {
				t.Errorf("page %s has PAGE_LIMIT without stop marker", path)
			}
		default:
			t.Errorf("page %s result = %s", path, page.ResultKind)
		}
	}
	if successes != 1 || pageLimits != 1 {
		t.Fatalf("redirect results = %d success and %d page-limit, want 1 and 1", successes, pageLimits)
	}
	if len(outcome.result.Problems) != 0 {
		t.Fatalf("page-limit stop must not be broken: %+v", outcome.result.Problems)
	}
}

func newConcurrentInspector(
	t *testing.T,
	startURL string,
	maxDepth int,
	maxPages int,
	concurrency int,
) *crawler.Inspector {
	t.Helper()
	inspector, err := crawler.New(crawler.Config{
		StartURL:       startURL,
		RequestTimeout: 2 * time.Second,
		MaxDepth:       maxDepth,
		MaxPages:       maxPages,
		MaxRedirects:   10,
		Concurrency:    concurrency,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	return inspector
}

func updateAtomicMaximum(maximum *atomic.Int32, candidate int32) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
