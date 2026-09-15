package crawler_test

import (
	"context"
	"errors"
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

type requestRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *requestRecorder) add(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
}

func (r *requestRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func TestCrawlUsesDFSAndKeepsAllSources(t *testing.T) {
	var externalRequests atomic.Int32
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer externalServer.Close()

	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w,
				`<a href="/a">A</a>`,
				`<a href="/b">B</a>`,
				fmt.Sprintf(`<a href="%s/outside">Outside</a>`, externalServer.URL),
			)
		case "/a":
			writeTestHTML(w,
				`<a href="/a-1">A1</a>`,
				`<a href="/shared#via-a">Shared</a>`,
			)
		case "/a-1":
			writeTestHTML(w, `<a href="/">Cycle</a>`)
		case "/b":
			writeTestHTML(w, `<a href="/shared">Shared</a>`)
		case "/shared":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 10, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/a", "/a-1", "/shared", "/b"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want strict DFS %v", got, wantOrder)
	}
	if got := externalRequests.Load(); got != 0 {
		t.Fatalf("external requests = %d, want 0", got)
	}

	sharedURL := server.URL + "/shared"
	wantSources := []string{server.URL + "/a", server.URL + "/b"}
	if got := result.SourcesByURL[sharedURL]; !reflect.DeepEqual(got, wantSources) {
		t.Fatalf("shared sources = %v, want %v", got, wantSources)
	}
	if len(result.Pages) != len(wantOrder) {
		t.Fatalf("pages checked = %d, want %d", len(result.Pages), len(wantOrder))
	}
}

func TestCrawlReusesPageWhenShorterPathIsFoundLater(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/long">Long</a>`, `<a href="/short">Short</a>`)
		case "/long":
			writeTestHTML(w, `<a href="/middle">Middle</a>`)
		case "/middle":
			writeTestHTML(w, `<a href="/target">Target</a>`)
		case "/target":
			writeTestHTML(w, `<a href="/leaf">Leaf</a>`)
		case "/short":
			writeTestHTML(w, `<a href="/target">Target</a>`)
		case "/leaf":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 3, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/long", "/middle", "/target", "/short", "/leaf"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if countPath(requests.snapshot(), "/target") != 1 {
		t.Fatalf("target must be fetched exactly once: %v", requests.snapshot())
	}

	target := findPage(t, result, server.URL+"/target")
	if target.Depth != 2 || target.FetchDepth != 3 {
		t.Fatalf("target depths = minimum %d, fetch %d; want 2 and 3", target.Depth, target.FetchDepth)
	}
	if got := result.DepthByURL[server.URL+"/leaf"]; got != 3 {
		t.Fatalf("leaf depth = %d, want 3", got)
	}
}

func TestCrawlHonorsMaximumDepth(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/a">A</a>`)
		case "/a":
			writeTestHTML(w, `<a href="/b">B</a>`)
		case "/b":
			writeTestHTML(w, `<a href="/too-deep">Too deep</a>`)
		case "/too-deep":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/a", "/b"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	tooDeepURL := server.URL + "/too-deep"
	if got := result.DepthByURL[tooDeepURL]; got != 3 {
		t.Fatalf("too-deep URL depth = %d, want 3", got)
	}
	if _, exists := result.SourcesByURL[tooDeepURL]; !exists {
		t.Fatal("link beyond depth limit must remain in the discovered graph")
	}
}

func TestCrawlWithZeroDepthChecksOnlyStartPage(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		if r.URL.Path == "/" {
			writeTestHTML(w, `<a href="/child">Child</a>`)
			return
		}
		writeTestHTML(w)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 0, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if got := requests.snapshot(); !reflect.DeepEqual(got, []string{"/"}) {
		t.Fatalf("requests = %v, want only the start page", got)
	}
	if result.LinksDiscovered != 1 {
		t.Fatalf("links discovered = %d, want 1", result.LinksDiscovered)
	}
	if got := result.DepthByURL[server.URL+"/child"]; got != 1 {
		t.Fatalf("child depth = %d, want 1", got)
	}
}

func TestTooDeepDiscoveryDoesNotBlockLaterShallowPath(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/long">Long</a>`, `<a href="/short">Short</a>`)
		case "/long":
			writeTestHTML(w, `<a href="/middle">Middle</a>`)
		case "/middle":
			writeTestHTML(w, `<a href="/target">Target too deep here</a>`)
		case "/short":
			writeTestHTML(w, `<a href="/target">Target allowed here</a>`)
		case "/target":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/long", "/middle", "/short", "/target"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if got := result.DepthByURL[server.URL+"/target"]; got != 2 {
		t.Fatalf("target depth = %d, want 2", got)
	}
}

func TestCrawlHonorsMaximumPages(t *testing.T) {
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

	inspector := newTestInspector(t, server.URL+"/", 10, 2, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/a"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if !result.MaxPagesReached {
		t.Fatal("MaxPagesReached = false, want true")
	}
}

func TestMaxPagesStillAllowsCachedDepthRelaxation(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/long-1">Long</a>`, `<a href="/short-parent">Short</a>`)
		case "/long-1":
			writeTestHTML(w, `<a href="/long-2">Long 2</a>`)
		case "/long-2":
			writeTestHTML(w, `<a href="/visited">Visited</a>`)
		case "/visited":
			writeTestHTML(w, `<a href="/child">Child</a>`)
		case "/short-parent":
			writeTestHTML(w,
				`<a href="/new-page">New page</a>`,
				`<a href="/visited">Visited by shorter path</a>`,
			)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 3, 5, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/long-1", "/long-2", "/visited", "/short-parent"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if !result.MaxPagesReached {
		t.Fatal("MaxPagesReached = false, want true")
	}
	if got := result.DepthByURL[server.URL+"/visited"]; got != 2 {
		t.Fatalf("visited page depth = %d, want 2", got)
	}
	if got := result.DepthByURL[server.URL+"/child"]; got != 3 {
		t.Fatalf("cached child depth = %d, want 3 after depth relaxation", got)
	}
	if countPath(requests.snapshot(), "/child") != 0 {
		t.Fatal("child must not be fetched after max-pages is reached")
	}
}

func TestCrawlChecksNonHTMLWithoutParsingIt(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/document.pdf">PDF</a>`)
		case "/document.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, `<a href="/must-not-be-requested">Fake HTML link</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 5, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{"/", "/document.pdf"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if _, exists := result.SourcesByURL[server.URL+"/must-not-be-requested"]; exists {
		t.Fatal("link-like text from a PDF must not be parsed as HTML")
	}
}

func TestCrawlContinuesAfterOneRequestTimesOut(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/slow">Slow</a>`, `<a href="/fast">Fast</a>`)
		case "/slow":
			<-r.Context().Done()
		case "/fast":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 20, 30*time.Millisecond)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl must continue after a per-request timeout: %v", err)
	}

	wantOrder := []string{"/", "/slow", "/fast"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	slowPage := findPage(t, result, server.URL+"/slow")
	if !errors.Is(slowPage.FetchError, context.DeadlineExceeded) {
		t.Fatalf("slow FetchError = %v, want context deadline exceeded", slowPage.FetchError)
	}
}

func TestCrawlStopsOnParentCancellation(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/slow">Slow</a>`, `<a href="/never">Never</a>`)
		case "/slow":
			startedOnce.Do(func() { close(started) })
			<-r.Context().Done()
		case "/never":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 20, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	type crawlResponse struct {
		result crawler.CrawlResult
		err    error
	}
	completed := make(chan crawlResponse, 1)
	go func() {
		result, err := inspector.Crawl(ctx)
		completed <- crawlResponse{result: result, err: err}
	}()

	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("slow request did not start")
	}

	select {
	case response := <-completed:
		if !errors.Is(response.err, context.Canceled) {
			t.Fatalf("Crawl error = %v, want context canceled", response.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Crawl did not stop after cancellation")
	}

	if countPath(requests.snapshot(), "/never") != 0 {
		t.Fatalf("page after cancelled request was fetched: %v", requests.snapshot())
	}
}

func TestCrawlWithCancelledContextMakesNoRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTestHTML(w)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 20, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := inspector.Crawl(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Crawl error = %v, want context canceled", err)
	}
	if len(result.Pages) != 0 {
		t.Fatalf("pages checked = %d, want 0", len(result.Pages))
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func newTestInspector(
	t *testing.T,
	startURL string,
	maxDepth int,
	maxPages int,
	timeout time.Duration,
) *crawler.Inspector {
	t.Helper()
	inspector, err := crawler.New(crawler.Config{
		StartURL:       startURL,
		RequestTimeout: timeout,
		MaxDepth:       maxDepth,
		MaxPages:       maxPages,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return inspector
}

func writeTestHTML(w http.ResponseWriter, links ...string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	fmt.Fprint(w, "<!doctype html><html><body>")
	for _, link := range links {
		fmt.Fprint(w, link)
	}
	fmt.Fprint(w, "</body></html>")
}

func findPage(t *testing.T, result crawler.CrawlResult, pageURL string) crawler.PageResult {
	t.Helper()
	for _, page := range result.Pages {
		if page.URL == pageURL {
			return page
		}
	}
	t.Fatalf("page %s was not checked", pageURL)
	return crawler.PageResult{}
}

func countPath(paths []string, target string) int {
	count := 0
	for _, path := range paths {
		if path == target {
			count++
		}
	}
	return count
}
