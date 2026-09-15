package crawler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
	"example.com/graph-test-site-go/internal/site"
)

func TestCrawlCurrentGraphSiteAtDepthThree(t *testing.T) {
	siteHandler, err := site.NewHandler(site.Config{SlowDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("site.NewHandler: %v", err)
	}

	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		siteHandler.ServeHTTP(w, r)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/index.html", 3, 100, 5*time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantOrder := []string{
		"/index.html",
		"/a.html",
		"/b.html",
		"/c.html",
		"/assets/pdf/b.pdf",
		"/assets/pdf/a.pdf",
		"/depth-1.html",
		"/depth-2.html",
		"/depth-3.html",
		"/assets/images/depth-2.jpg",
		"/assets/images/depth-1.png",
		"/hub.html",
		"/redirect-once",
		"/redirect-chain/start",
		"/slow.html",
		"/missing-page.html",
		"/server-error",
	}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order =\n%v\nwant strict DFS order =\n%v", got, wantOrder)
	}
	if len(result.Pages) != 17 {
		t.Fatalf("pages checked = %d, want 17", len(result.Pages))
	}
	if result.LinksDiscovered != 27 {
		t.Fatalf("links discovered = %d, want 27", result.LinksDiscovered)
	}
	if len(result.SourcesByURL) != 19 {
		t.Fatalf("unique normalized target URLs = %d, want 19", len(result.SourcesByURL))
	}
	if result.MaxPagesReached {
		t.Fatal("MaxPagesReached = true, want false")
	}

	internalLinks, externalLinks := linkKindCounts(result)
	if internalLinks != 26 || externalLinks != 1 {
		t.Fatalf(
			"link occurrences = %d internal and %d external, want 26 and 1",
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
			t.Fatal("deep target was checked despite maximum depth 3")
		}
	}

	wantHubSources := []string{
		server.URL + "/index.html",
		server.URL + "/a.html",
		server.URL + "/b.html",
		server.URL + "/slow.html",
	}
	if got := result.SourcesByURL[server.URL+"/hub.html"]; !reflect.DeepEqual(got, wantHubSources) {
		t.Fatalf("hub sources = %v, want %v", got, wantHubSources)
	}

	cPage := findPage(t, result, server.URL+"/c.html")
	if cPage.Depth != 2 || cPage.FetchDepth != 3 {
		t.Fatalf("C depths = minimum %d, fetch %d; want 2 and 3", cPage.Depth, cPage.FetchDepth)
	}
}

func linkKindCounts(result crawler.CrawlResult) (internal int, external int) {
	for _, page := range result.Pages {
		for _, link := range page.Links {
			switch link.Kind {
			case crawler.LinkInternal:
				internal++
			case crawler.LinkExternal:
				external++
			}
		}
	}
	return internal, external
}
