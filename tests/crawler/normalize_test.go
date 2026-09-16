package crawler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestNormalizeURLRulesThroughPublicAPI(t *testing.T) {
	markup := `<!doctype html><html><body>
<a href="/about">root relative</a>
<a href="../contacts">parent relative</a>
<a href="products?id=10">query retained</a>
<a href="#section">fragment removed</a>
<a href="/about/">trailing slash retained</a>
<a href="https://EXAMPLE.com:0443/help">default port removed</a>
<a href="https://EXAMPLE.com:08443/help">numeric port normalized</a>
<a href=" ">empty href means current page</a>
<a href="mailto:test@example.com">unsupported scheme</a>
<a href="https://example.com:65536/help">invalid port</a>
<a href="%zz">malformed URL</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		fmt.Fprint(w, markup)
	}))
	defer server.Close()

	startURL := server.URL + "/docs/index.html?old=1"
	inspector := newTestInspector(t, startURL, 0, 1, time.Second)
	page, err := inspector.InspectStart(context.Background())
	if err != nil {
		t.Fatalf("InspectStart: %v", err)
	}

	wantURLs := map[string]string{
		"/about":                         server.URL + "/about",
		"../contacts":                    server.URL + "/contacts",
		"products?id=10":                 server.URL + "/docs/products?id=10",
		"#section":                       startURL,
		"/about/":                        server.URL + "/about/",
		"https://EXAMPLE.com:0443/help":  "https://example.com/help",
		"https://EXAMPLE.com:08443/help": "https://example.com:8443/help",
		" ":                              startURL,
	}
	if len(page.Links) != len(wantURLs) {
		t.Fatalf("normalized links = %d, want %d: %+v", len(page.Links), len(wantURLs), page.Links)
	}
	for _, link := range page.Links {
		wantURL, exists := wantURLs[link.RawHref]
		if !exists {
			t.Errorf("unexpected normalized href %q", link.RawHref)
			continue
		}
		if link.URL != wantURL {
			t.Errorf("href %q normalized to %q, want %q", link.RawHref, link.URL, wantURL)
		}
	}

	wantSkipped := map[string]string{
		"mailto:test@example.com":        "unsupported URL scheme",
		"https://example.com:65536/help": "invalid URL port",
		"%zz":                            "invalid URL escape",
	}
	if len(page.Skipped) != len(wantSkipped) {
		t.Fatalf("skipped links = %d, want %d: %+v", len(page.Skipped), len(wantSkipped), page.Skipped)
	}
	for _, skipped := range page.Skipped {
		wantReason, exists := wantSkipped[skipped.RawHref]
		if !exists {
			t.Errorf("unexpected skipped href %q", skipped.RawHref)
			continue
		}
		if !strings.Contains(skipped.Reason, wantReason) {
			t.Errorf("href %q reason = %q, want it to contain %q", skipped.RawHref, skipped.Reason, wantReason)
		}
	}
}

func TestScopeUsesSchemeHostnameAndEffectivePortThroughPublicAPI(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		parsedServerURL, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse test server URL: %v", err)
		}
		port, err := strconv.Atoi(parsedServerURL.Port())
		if err != nil {
			t.Fatalf("parse test server port: %v", err)
		}
		otherPort := port + 1
		if port == 65535 {
			otherPort = port - 1
		}

		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		fmt.Fprintf(w, `<a href="%s/about">same origin</a>`, server.URL)
		fmt.Fprintf(w, `<a href="http://%s:%d/about">other port</a>`, parsedServerURL.Hostname(), otherPort)
		fmt.Fprintf(w, `<a href="https://%s/about">other scheme</a>`, parsedServerURL.Host)
		fmt.Fprint(w, `<a href="http://example.invalid/about">other hostname</a>`)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/index.html", 0, 1, time.Second)
	page, err := inspector.InspectStart(context.Background())
	if err != nil {
		t.Fatalf("InspectStart: %v", err)
	}
	if len(page.Links) != 4 {
		t.Fatalf("links = %d, want 4", len(page.Links))
	}
	if page.Links[0].Kind != crawler.LinkInternal {
		t.Errorf("same-origin link kind = %s, want INTERNAL", page.Links[0].Kind)
	}
	for _, link := range page.Links[1:] {
		if link.Kind != crawler.LinkExternal {
			t.Errorf("link %q kind = %s, want EXTERNAL", link.RawHref, link.Kind)
		}
	}
}

func TestStartURLCanonicalizationThroughCrawlResult(t *testing.T) {
	inspector, err := crawler.New(crawler.Config{
		StartURL:       "HTTP://EXAMPLE.COM:080#fragment",
		RequestTimeout: time.Second,
		MaxDepth:       0,
		MaxPages:       1,
		MaxRedirects:   10,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, crawlErr := inspector.Crawl(ctx)
	if !errors.Is(crawlErr, context.Canceled) {
		t.Fatalf("Crawl error = %v, want context canceled", crawlErr)
	}
	if result.StartURL != "http://example.com/" {
		t.Fatalf("normalized start URL = %q, want %q", result.StartURL, "http://example.com/")
	}
}

func TestCrawlerConfigValidation(t *testing.T) {
	tests := []crawler.Config{
		{StartURL: "", RequestTimeout: 1, MaxDepth: 1, MaxPages: 1, MaxRedirects: 10},
		{StartURL: "/relative", RequestTimeout: 1, MaxDepth: 1, MaxPages: 1, MaxRedirects: 10},
		{StartURL: "ftp://example.com", RequestTimeout: 1, MaxDepth: 1, MaxPages: 1, MaxRedirects: 10},
		{StartURL: "http://example.com", RequestTimeout: 0, MaxDepth: 1, MaxPages: 1, MaxRedirects: 10},
		{StartURL: "http://example.com", RequestTimeout: 1, MaxDepth: -1, MaxPages: 1, MaxRedirects: 10},
		{StartURL: "http://example.com", RequestTimeout: 1, MaxDepth: 1, MaxPages: 0, MaxRedirects: 10},
		{StartURL: "http://example.com", RequestTimeout: 1, MaxDepth: 1, MaxPages: 1, MaxRedirects: -1},
	}
	for _, config := range tests {
		if err := config.Validate(); err == nil {
			t.Errorf("Config.Validate(%+v) error = nil", config)
		}
	}
}
