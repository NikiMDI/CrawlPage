package crawler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer externalServer.Close()
	externalURL := strings.Replace(externalServer.URL, "127.0.0.1", "localhost", 1)

	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w,
				`<a href="/a">A</a>`,
				`<a href="/b">B</a>`,
				fmt.Sprintf(`<a href="%s/outside">Outside</a>`, externalURL),
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
	if len(result.Problems) != 0 {
		t.Fatalf("unrequested external 500 must not become a problem: %+v", result.Problems)
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

func TestPageLimitStillAllowsCachedRedirectTarget(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(
				w,
				`<a href="/old">Old</a>`,
				`<a href="/uncached">Uncached</a>`,
				`<a href="/target">Target</a>`,
			)
		case "/old":
			w.Header().Set("Location", "/target")
			w.WriteHeader(http.StatusFound)
		case "/target":
			writeTestHTML(w, `<a href="/leaf">Leaf</a>`)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 2, 3, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if got := requests.snapshot(); !reflect.DeepEqual(got, []string{"/", "/old", "/target"}) {
		t.Fatalf("HTTP requests = %v, want root, redirect source, and target", got)
	}
	if result.PagesChecked != 3 || !result.MaxPagesReached {
		t.Fatalf(
			"pages checked/max reached = %d/%t, want 3/true",
			result.PagesChecked,
			result.MaxPagesReached,
		)
	}
	target := findPage(t, result, server.URL+"/target")
	if target.ResultKind != crawler.ResultSuccess || !target.HTMLParsed {
		t.Fatalf("cached direct target = %+v, want parsed SUCCESS", target)
	}
	if countPath(requests.snapshot(), "/target") != 1 {
		t.Fatalf("redirect target was fetched more than once: %v", requests.snapshot())
	}
	for _, page := range result.Pages {
		if page.URL == server.URL+"/uncached" || page.URL == server.URL+"/leaf" {
			t.Fatalf("uncached URL was processed after max-pages: %+v", page)
		}
	}
}

func TestCrawlChecksBinaryContentWithoutParsingIt(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/binary">Binary</a>`)
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
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

	wantOrder := []string{"/", "/binary"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	if _, exists := result.SourcesByURL[server.URL+"/must-not-be-requested"]; exists {
		t.Fatal("link-like text from binary content must not be parsed as HTML")
	}
	if result.PagesChecked != 2 {
		t.Fatalf("pages checked = %d, want both requested HTTP URLs", result.PagesChecked)
	}
}

func TestPDFLinkIsSkippedWithoutHTTPRequest(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w,
				`<a href="/page">Page</a>`,
				`<a href="/document.PDF?download=1">PDF</a>`,
			)
		case "/page":
			writeTestHTML(w)
		case "/document.PDF":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 1, 2, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantRequests := []string{"/", "/page"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("requests = %v, want %v", got, wantRequests)
	}
	if result.PagesChecked != 2 {
		t.Fatalf("pages checked = %d, want only the two requested HTML pages", result.PagesChecked)
	}
	root := findPage(t, result, server.URL+"/")
	if len(root.Skipped) != 1 ||
		root.Skipped[0].RawHref != "/document.PDF?download=1" ||
		!strings.Contains(root.Skipped[0].Reason, "PDF") {
		t.Fatalf("skipped links = %+v, want the PDF link", root.Skipped)
	}
	for _, page := range result.Pages {
		if strings.Contains(strings.ToLower(page.URL), ".pdf") {
			t.Fatalf("PDF unexpectedly appears in checked pages: %+v", page)
		}
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

	inspector := newTestInspector(t, server.URL+"/", 2, 20, 250*time.Millisecond)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl must continue after a per-request timeout: %v", err)
	}

	wantOrder := []string{"/", "/slow", "/fast"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
	slowPage := findPage(t, result, server.URL+"/slow")
	if slowPage.ResultKind != crawler.ResultTimeout {
		t.Fatalf("slow result = %s, want TIMEOUT", slowPage.ResultKind)
	}
	if slowPage.Status != "" {
		t.Fatalf("slow status = %q, want no HTTP status", slowPage.Status)
	}
	if !strings.Contains(slowPage.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("slow error = %q, want context deadline exceeded", slowPage.Error)
	}
	if fastPage := findPage(t, result, server.URL+"/fast"); fastPage.ResultKind != crawler.ResultSuccess {
		t.Fatalf("fast result = %s, want SUCCESS", fastPage.ResultKind)
	}
	if len(result.Problems) != 1 || result.Problems[0].URL != server.URL+"/slow" {
		t.Fatalf("problems = %+v, want only the slow URL", result.Problems)
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
		if len(response.result.Problems) != 0 {
			t.Fatalf("parent cancellation must not create a broken-link problem: %+v", response.result.Problems)
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

	inspector := newConcurrentInspector(t, server.URL+"/", 2, 20, 8)
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
		MaxRedirects:   10,
		Concurrency:    1,
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
