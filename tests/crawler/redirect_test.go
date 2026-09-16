package crawler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func TestRedirectChainKeepsEveryHopAndFinalResult(t *testing.T) {
	var requests requestRecorder
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", "/middle")
			w.WriteHeader(http.StatusFound)
		case "/middle":
			w.Header().Set("Location", serverURL+"/final")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/final":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/start", 0, 10, time.Second, 2)
	page, err := inspector.InspectStart(context.Background())
	if err != nil {
		t.Fatalf("InspectStart: %v", err)
	}

	if want := []string{"/start", "/middle", "/final"}; !reflect.DeepEqual(requests.snapshot(), want) {
		t.Fatalf("request order = %v, want %v", requests.snapshot(), want)
	}
	wantChain := []crawler.RedirectHop{
		{URL: server.URL + "/start", Status: "302 Found", TargetURL: server.URL + "/middle"},
		{URL: server.URL + "/middle", Status: "301 Moved Permanently", TargetURL: server.URL + "/final"},
	}
	if !reflect.DeepEqual(page.RedirectChain, wantChain) {
		t.Fatalf("redirect chain = %+v, want %+v", page.RedirectChain, wantChain)
	}
	if page.FinalURL != server.URL+"/final" || page.ResultKind != crawler.ResultSuccess || page.Status != "200 OK" {
		t.Fatalf("final result = URL %q, kind %s, status %q", page.FinalURL, page.ResultKind, page.Status)
	}
}

func TestRedirectFinalHTMLUsesFinalURLAsLinkBaseAndSource(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/docs/start":
			w.Header().Set("Location", "../final/#section")
			w.WriteHeader(http.StatusFound)
		case "/final/":
			writeTestHTML(w, `<a href="child">Child</a>`)
		case "/final/child":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/docs/start", 1, 10, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if want := []string{"/docs/start", "/final/", "/final/child"}; !reflect.DeepEqual(requests.snapshot(), want) {
		t.Fatalf("request order = %v, want %v", requests.snapshot(), want)
	}
	startPage := findPage(t, result, server.URL+"/docs/start")
	if startPage.FinalURL != server.URL+"/final/" {
		t.Fatalf("final URL = %q, want fragment-free final URL", startPage.FinalURL)
	}
	if len(startPage.Links) != 1 {
		t.Fatalf("final links = %+v, want one link", startPage.Links)
	}
	link := startPage.Links[0]
	if link.SourceURL != server.URL+"/final/" || link.URL != server.URL+"/final/child" {
		t.Fatalf("final link = %+v, want source /final/ and target /final/child", link)
	}
	if got := result.DepthByURL[server.URL+"/final/child"]; got != 1 {
		t.Fatalf("child depth = %d, want 1; redirect must not increase depth", got)
	}
}

func TestStartRedirectContinuesWithFinalHTMLLinks(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			w.Header().Set("Location", "/index.html")
			w.WriteHeader(http.StatusFound)
		case "/index.html":
			writeTestHTML(w, `<a href="/page.html">Page</a>`)
		case "/page.html":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/", 1, 10, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if want := []string{"/", "/index.html", "/page.html"}; !reflect.DeepEqual(requests.snapshot(), want) {
		t.Fatalf("request order = %v, want %v", requests.snapshot(), want)
	}
	if got := result.DepthByURL[server.URL+"/page.html"]; got != 1 {
		t.Fatalf("page discovered in final HTML has depth %d, want 1", got)
	}
}

func TestRedirectCycleStopsWithoutRepeatingRequest(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/loop-a":
			w.Header().Set("Location", "/loop-b")
			w.WriteHeader(http.StatusFound)
		case "/loop-b":
			w.Header().Set("Location", "/loop-a")
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/loop-a", 0, 10, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if want := []string{"/loop-a", "/loop-b"}; !reflect.DeepEqual(requests.snapshot(), want) {
		t.Fatalf("request order = %v, want %v", requests.snapshot(), want)
	}
	page := result.Pages[0]
	if page.ResultKind != crawler.ResultRedirectError || len(page.RedirectChain) != 2 {
		t.Fatalf("loop result = %s, chain = %+v", page.ResultKind, page.RedirectChain)
	}
	if page.FinalURL != server.URL+"/loop-a" || !strings.Contains(page.Error, "cycle") {
		t.Fatalf("loop final/error = %q / %q", page.FinalURL, page.Error)
	}
	if !page.RedirectCycleDetected || page.Status != "301 Moved Permanently" {
		t.Fatalf("loop marker/status = %t / %q", page.RedirectCycleDetected, page.Status)
	}
	if len(result.Problems) != 1 || result.Problems[0].Kind != crawler.ResultRedirectError {
		t.Fatalf("loop problems = %+v, want one REDIRECT_ERROR", result.Problems)
	}
}

func TestMaximumRedirectsBoundary(t *testing.T) {
	t.Run("exact limit succeeds", func(t *testing.T) {
		var requests requestRecorder
		server := newRedirectSequenceServer(&requests, 2)
		defer server.Close()

		inspector := newRedirectInspector(t, server.URL+"/r1", 0, 10, time.Second, 2)
		page, err := inspector.InspectStart(context.Background())
		if err != nil {
			t.Fatalf("InspectStart: %v", err)
		}
		if page.ResultKind != crawler.ResultSuccess || len(page.RedirectChain) != 2 {
			t.Fatalf("result = %s, chain = %+v; want success with two hops", page.ResultKind, page.RedirectChain)
		}
		if countPath(requests.snapshot(), "/final") != 1 {
			t.Fatalf("final request count = %d, want 1", countPath(requests.snapshot(), "/final"))
		}
	})

	t.Run("one more redirect stops before target", func(t *testing.T) {
		var requests requestRecorder
		server := newRedirectSequenceServer(&requests, 3)
		defer server.Close()

		inspector := newRedirectInspector(t, server.URL+"/r1", 0, 10, time.Second, 2)
		result, err := inspector.Crawl(context.Background())
		if err != nil {
			t.Fatalf("Crawl: %v", err)
		}
		page := result.Pages[0]
		if page.ResultKind != crawler.ResultRedirectError || len(page.RedirectChain) != 3 {
			t.Fatalf("result = %s, chain = %+v; want limit error after observed third redirect", page.ResultKind, page.RedirectChain)
		}
		if countPath(requests.snapshot(), "/final") != 0 {
			t.Fatalf("final URL must not be requested after the limit: %v", requests.snapshot())
		}
		if !strings.Contains(page.Error, "limit is 2") {
			t.Fatalf("limit error = %q", page.Error)
		}
	})
}

func TestInvalidRedirectLocationIsReported(t *testing.T) {
	tests := []struct {
		name     string
		location *string
		contains string
	}{
		{name: "missing Location", contains: "no Location"},
		{name: "invalid Location", location: stringPointer("%zz"), contains: "invalid URL escape"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.location != nil {
					w.Header().Set("Location", *test.location)
				}
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()

			inspector := newRedirectInspector(t, server.URL, 0, 10, time.Second, 10)
			result, err := inspector.Crawl(context.Background())
			if err != nil {
				t.Fatalf("Crawl: %v", err)
			}
			page := result.Pages[0]
			if page.ResultKind != crawler.ResultRedirectError || len(result.Problems) != 1 {
				t.Fatalf("result/problems = %s / %+v", page.ResultKind, result.Problems)
			}
			if !strings.Contains(page.Error, test.contains) {
				t.Fatalf("error = %q, want substring %q", page.Error, test.contains)
			}
		})
	}
}

func TestRedirectToExternalOriginIsRecordedButNotRequested(t *testing.T) {
	var externalRequests atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalRequests.Add(1)
		writeTestHTML(w)
	}))
	defer external.Close()

	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", external.URL+"/landing")
		w.WriteHeader(http.StatusFound)
	}))
	defer internal.Close()

	inspector := newRedirectInspector(t, internal.URL, 0, 1, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	page := result.Pages[0]
	if page.ResultKind != crawler.ResultRedirect || !page.RedirectedOutsideScope {
		t.Fatalf("external redirect result = %s, outside = %t", page.ResultKind, page.RedirectedOutsideScope)
	}
	if page.FinalURL != external.URL+"/landing" || externalRequests.Load() != 0 {
		t.Fatalf("external final/request count = %q / %d", page.FinalURL, externalRequests.Load())
	}
	if len(result.Problems) != 0 {
		t.Fatalf("external redirect must not be broken: %+v", result.Problems)
	}
	if result.PagesChecked != 1 || result.MaxPagesReached {
		t.Fatalf(
			"external redirect pages/max reached = %d / %t, want 1 / false",
			result.PagesChecked,
			result.MaxPagesReached,
		)
	}
}

func TestRedirectFinalHTTPErrorKeepsOriginalLinkAndSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/r404">404</a>`, `<a href="/r500">500</a>`)
		case "/r404":
			w.Header().Set("Location", "/missing")
			w.WriteHeader(http.StatusFound)
		case "/missing":
			http.NotFound(w, r)
		case "/r500":
			w.Header().Set("Location", "/error")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/", 1, 10, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if len(result.Problems) != 2 {
		t.Fatalf("problems = %+v, want redirect-to-404 and redirect-to-500", result.Problems)
	}
	wantKinds := []crawler.ResultKind{crawler.ResultHTTP4XX, crawler.ResultHTTP5XX}
	for index, problem := range result.Problems {
		if problem.Kind != wantKinds[index] {
			t.Errorf("problem %d kind = %s, want %s", index, problem.Kind, wantKinds[index])
		}
		if !reflect.DeepEqual(problem.Sources, []string{server.URL + "/"}) {
			t.Errorf("problem %d sources = %v", index, problem.Sources)
		}
	}
	page404 := findPage(t, result, server.URL+"/r404")
	if page404.FinalURL != server.URL+"/missing" || page404.Status != "404 Not Found" {
		t.Fatalf("redirect 404 final = %q / %q", page404.FinalURL, page404.Status)
	}
	page500 := findPage(t, result, server.URL+"/r500")
	if page500.FinalURL != server.URL+"/error" || page500.Status != "500 Internal Server Error" {
		t.Fatalf("redirect 500 final = %q / %q", page500.FinalURL, page500.Status)
	}
}

func TestRedirectTargetAlsoLinkedDirectlyKeepsSeparateProblemAndSource(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/old">Old</a>`, `<a href="/source">Source</a>`)
		case "/old":
			w.Header().Set("Location", "/missing")
			w.WriteHeader(http.StatusFound)
		case "/source":
			writeTestHTML(w, `<a href="/missing">Missing directly</a>`)
		case "/missing":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/", 2, 10, time.Second, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	if len(result.Problems) != 2 {
		t.Fatalf("problems = %+v, want separate /old and /missing problems", result.Problems)
	}
	if result.Problems[0].URL != server.URL+"/old" ||
		!reflect.DeepEqual(result.Problems[0].Sources, []string{server.URL + "/"}) {
		t.Fatalf("redirect problem = %+v", result.Problems[0])
	}
	if result.Problems[1].URL != server.URL+"/missing" ||
		!reflect.DeepEqual(result.Problems[1].Sources, []string{server.URL + "/source"}) {
		t.Fatalf("direct problem = %+v", result.Problems[1])
	}
	if got := countPath(requests.snapshot(), "/missing"); got != 1 {
		t.Fatalf("shared broken target request count = %d, want 1", got)
	}
	if result.PagesChecked != 4 {
		t.Fatalf("pages checked = %d, want 4 unique URLs", result.PagesChecked)
	}
}

func TestMaxPagesAppliesInsideRedirectChain(t *testing.T) {
	tests := []struct {
		name             string
		maxPages         int
		wantRequests     []string
		wantKind         crawler.ResultKind
		wantHops         int
		wantLimitReached bool
	}{
		{
			name:             "stop before first target",
			maxPages:         1,
			wantRequests:     []string{"/r1"},
			wantKind:         crawler.ResultPageLimit,
			wantHops:         1,
			wantLimitReached: true,
		},
		{
			name:             "stop before final target",
			maxPages:         2,
			wantRequests:     []string{"/r1", "/r2"},
			wantKind:         crawler.ResultPageLimit,
			wantHops:         2,
			wantLimitReached: true,
		},
		{
			name:             "exact page budget succeeds",
			maxPages:         3,
			wantRequests:     []string{"/r1", "/r2", "/final"},
			wantKind:         crawler.ResultSuccess,
			wantHops:         2,
			wantLimitReached: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests requestRecorder
			server := newRedirectSequenceServer(&requests, 2)
			defer server.Close()

			inspector := newRedirectInspector(
				t,
				server.URL+"/r1",
				0,
				test.maxPages,
				time.Second,
				10,
			)
			result, err := inspector.Crawl(context.Background())
			if err != nil {
				t.Fatalf("Crawl: %v", err)
			}

			if got := requests.snapshot(); !reflect.DeepEqual(got, test.wantRequests) {
				t.Fatalf("requests = %v, want %v", got, test.wantRequests)
			}
			if result.PagesChecked != len(test.wantRequests) {
				t.Fatalf("pages checked = %d, want %d", result.PagesChecked, len(test.wantRequests))
			}
			if result.MaxPagesReached != test.wantLimitReached {
				t.Fatalf("MaxPagesReached = %t, want %t", result.MaxPagesReached, test.wantLimitReached)
			}
			if len(result.Pages) != 1 {
				t.Fatalf("logical pages = %d, want 1", len(result.Pages))
			}
			page := result.Pages[0]
			if page.ResultKind != test.wantKind || len(page.RedirectChain) != test.wantHops {
				t.Fatalf(
					"result/hops = %s / %d, want %s / %d",
					page.ResultKind,
					len(page.RedirectChain),
					test.wantKind,
					test.wantHops,
				)
			}
			if page.StoppedByPageLimit != test.wantLimitReached {
				t.Fatalf(
					"StoppedByPageLimit = %t, want %t",
					page.StoppedByPageLimit,
					test.wantLimitReached,
				)
			}
			if len(result.Problems) != 0 {
				t.Fatalf("page-limit stop must not be broken: %+v", result.Problems)
			}
		})
	}
}

func TestRedirectTargetUsesSharedFetchCache(t *testing.T) {
	tests := []struct {
		name      string
		rootLinks []string
	}{
		{name: "redirect before direct link", rootLinks: []string{`<a href="/old">Old</a>`, `<a href="/target">Target</a>`}},
		{name: "direct link before redirect", rootLinks: []string{`<a href="/target">Target</a>`, `<a href="/old">Old</a>`}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests requestRecorder
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.add(r.URL.Path)
				switch r.URL.Path {
				case "/":
					writeTestHTML(w, test.rootLinks...)
				case "/old":
					w.Header().Set("Location", "/target")
					w.WriteHeader(http.StatusFound)
				case "/target":
					writeTestHTML(w, `<a href="/leaf">Leaf</a>`)
				case "/leaf":
					writeTestHTML(w)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			inspector := newRedirectInspector(t, server.URL+"/", 2, 10, time.Second, 10)
			result, err := inspector.Crawl(context.Background())
			if err != nil {
				t.Fatalf("Crawl: %v", err)
			}

			if got := countPath(requests.snapshot(), "/target"); got != 1 {
				t.Fatalf("target request count = %d, want 1; all requests: %v", got, requests.snapshot())
			}
			if result.PagesChecked != 4 {
				t.Fatalf("pages checked = %d, want 4 unique URLs", result.PagesChecked)
			}
			if result.LinksDiscovered != 3 {
				t.Fatalf("links discovered = %d, want 3 without duplicate target HTML", result.LinksDiscovered)
			}
		})
	}
}

func TestUnfollowedRedirectDoesNotHideDirectTargetLinks(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/old">Old</a>`, `<a href="/target">Target</a>`)
		case "/old":
			w.Header().Set("Location", "/target")
			w.WriteHeader(http.StatusFound)
		case "/target":
			writeTestHTML(w, `<a href="/leaf">Leaf</a>`)
		case "/leaf":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/", 2, 10, time.Second, 0)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	oldPage := findPage(t, result, server.URL+"/old")
	if oldPage.ResultKind != crawler.ResultRedirectError || oldPage.HTMLParsed {
		t.Fatalf("unfollowed redirect = %+v, want unparsed REDIRECT_ERROR", oldPage)
	}
	targetPage := findPage(t, result, server.URL+"/target")
	if !targetPage.HTMLParsed || len(targetPage.Links) != 1 {
		t.Fatalf("direct target page = %+v, want one parsed link", targetPage)
	}
	if result.LinksDiscovered != 3 {
		t.Fatalf("links discovered = %d, want root 2 + target 1", result.LinksDiscovered)
	}
	if got := countPath(requests.snapshot(), "/leaf"); got != 1 {
		t.Fatalf("leaf request count = %d, want 1; requests: %v", got, requests.snapshot())
	}
}

func TestTimeoutInsideRedirectChainIsAProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			w.Header().Set("Location", "/slow")
			w.WriteHeader(http.StatusFound)
		case "/slow":
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newRedirectInspector(t, server.URL+"/go", 0, 10, 50*time.Millisecond, 10)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	page := result.Pages[0]
	if page.ResultKind != crawler.ResultTimeout || page.FinalURL != server.URL+"/slow" {
		t.Fatalf("timeout result = %s at %q", page.ResultKind, page.FinalURL)
	}
	if page.Status != "" || len(page.RedirectChain) != 1 || !strings.Contains(page.Error, "deadline exceeded") {
		t.Fatalf("timeout page = %+v", page)
	}
	if len(result.Problems) != 1 || result.Problems[0].URL != server.URL+"/go" {
		t.Fatalf("timeout problems = %+v", result.Problems)
	}
}

func newRedirectInspector(
	t *testing.T,
	startURL string,
	maxDepth int,
	maxPages int,
	timeout time.Duration,
	maxRedirects int,
) *crawler.Inspector {
	t.Helper()
	inspector, err := crawler.New(crawler.Config{
		StartURL:       startURL,
		RequestTimeout: timeout,
		MaxDepth:       maxDepth,
		MaxPages:       maxPages,
		MaxRedirects:   maxRedirects,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	return inspector
}

func newRedirectSequenceServer(requests *requestRecorder, redirectCount int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		for index := 1; index <= redirectCount; index++ {
			if r.URL.Path != fmt.Sprintf("/r%d", index) {
				continue
			}
			target := "/final"
			if index < redirectCount {
				target = fmt.Sprintf("/r%d", index+1)
			}
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path == "/final" {
			writeTestHTML(w)
			return
		}
		http.NotFound(w, r)
	}))
}

func stringPointer(value string) *string {
	return &value
}
