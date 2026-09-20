package crawler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
	"example.com/graph-test-site-go/internal/site"
)

func TestFinalAcceptanceAgainstGraphSite(t *testing.T) {
	siteHandler, err := site.NewHandler(site.Config{SlowDelay: 2 * time.Second})
	if err != nil {
		t.Fatalf("site.NewHandler: %v", err)
	}

	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		siteHandler.ServeHTTP(w, r)
	}))
	defer server.Close()

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/index.html",
		RequestTimeout: 300 * time.Millisecond,
		MaxDepth:       3,
		MaxPages:       100,
		MaxRedirects:   10,
		Concurrency:    4,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}

	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if result.Concurrency != 4 || result.MaxDepth != 3 || result.MaxPages != 100 {
		t.Fatalf(
			"reported limits = concurrency %d, depth %d, pages %d",
			result.Concurrency,
			result.MaxDepth,
			result.MaxPages,
		)
	}
	if result.Elapsed <= 0 {
		t.Fatal("elapsed time was not recorded")
	}
	if result.PagesChecked != 14 {
		t.Fatalf("pages checked = %d, want 14 unique HTTP URLs", result.PagesChecked)
	}
	if len(result.Pages) != 13 {
		t.Fatalf("logical page results = %d, want 13", len(result.Pages))
	}
	if result.LinksDiscovered != 22 {
		t.Fatalf("links discovered = %d, want 22", result.LinksDiscovered)
	}
	if len(result.SourcesByURL) != 15 {
		t.Fatalf("unique normalized link targets = %d, want 15", len(result.SourcesByURL))
	}
	if result.MaxPagesReached {
		t.Fatal("max-pages was unexpectedly reached")
	}

	resultCounts := make(map[crawler.ResultKind]int)
	for _, page := range result.Pages {
		resultCounts[page.ResultKind]++
	}
	wantResultCounts := map[crawler.ResultKind]int{
		crawler.ResultSuccess:       10,
		crawler.ResultHTTP4XX:       1,
		crawler.ResultHTTP5XX:       1,
		crawler.ResultTimeout:       1,
		crawler.ResultNetworkError:  0,
		crawler.ResultRedirectError: 0,
	}
	for kind, want := range wantResultCounts {
		if got := resultCounts[kind]; got != want {
			t.Errorf("%s results = %d, want %d", kind, got, want)
		}
	}

	wantProblems := map[string]crawler.ResultKind{
		server.URL + "/missing-page.html": crawler.ResultHTTP4XX,
		server.URL + "/server-error":      crawler.ResultHTTP5XX,
		server.URL + "/slow.html":         crawler.ResultTimeout,
	}
	if len(result.Problems) != len(wantProblems) {
		t.Fatalf("problems = %+v, want exactly 404, 500, and timeout", result.Problems)
	}
	for _, problem := range result.Problems {
		wantKind, exists := wantProblems[problem.URL]
		if !exists || problem.Kind != wantKind {
			t.Errorf("unexpected problem: %+v", problem)
		}
		if len(problem.Sources) == 0 {
			t.Errorf("problem %s has no source page", problem.URL)
		}
	}

	redirectOnce := findPage(t, result, server.URL+"/redirect-once")
	if redirectOnce.ResultKind != crawler.ResultSuccess ||
		len(redirectOnce.RedirectChain) != 1 ||
		redirectOnce.FinalURL != server.URL+"/hub.html" {
		t.Errorf("single redirect result = %+v", redirectOnce)
	}
	redirectChain := findPage(t, result, server.URL+"/redirect-chain/start")
	if redirectChain.ResultKind != crawler.ResultSuccess ||
		len(redirectChain.RedirectChain) != 2 ||
		redirectChain.FinalURL != server.URL+"/c.html" {
		t.Errorf("two-hop redirect result = %+v", redirectChain)
	}

	internalLinks, externalLinks := linkKindCounts(result)
	if internalLinks != 21 || externalLinks != 1 {
		t.Fatalf(
			"link occurrences = %d internal and %d external, want 21 and 1",
			internalLinks,
			externalLinks,
		)
	}

	deepTargetURL := server.URL + "/deep-target.html"
	if got := result.DepthByURL[deepTargetURL]; got != 4 {
		t.Fatalf("deep target depth = %d, want 4", got)
	}
	for _, page := range result.Pages {
		if page.URL == deepTargetURL {
			t.Fatal("page beyond maximum depth was requested")
		}
	}

	assertSameStrings(t, result.SourcesByURL[server.URL+"/hub.html"], []string{
		server.URL + "/index.html",
		server.URL + "/a.html",
		server.URL + "/b.html",
	})

	requestedPaths := requests.snapshot()
	for _, path := range []string{"/a.html", "/b.html", "/c.html"} {
		if got := countPath(requestedPaths, path); got != 1 {
			t.Errorf("cycle page %s requested %d times, want once", path, got)
		}
	}
	if got := countPath(requestedPaths, "/deep-target.html"); got != 0 {
		t.Errorf("deep target requests = %d, want 0", got)
	}
}

func assertSameStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	if len(gotCopy) != len(wantCopy) {
		t.Fatalf("strings = %v, want %v", gotCopy, wantCopy)
	}
	for index := range gotCopy {
		if gotCopy[index] != wantCopy[index] {
			t.Fatalf("strings = %v, want %v", gotCopy, wantCopy)
		}
	}
}
