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

func TestCrawlClassifiesHTTPResults(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w,
				`<a href="/ok">OK</a>`,
				`<a href="/empty">No content</a>`,
				`<a href="/redirect">Redirect</a>`,
				`<a href="/client-error">Client error</a>`,
				`<a href="/server-error">Server error</a>`,
				`<a href="/other">Other status</a>`,
			)
		case "/ok":
			writeTestHTML(w)
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/redirect":
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(http.StatusFound)
			fmt.Fprint(w, `<a href="/redirect-body">not parsed yet</a>`)
		case "/client-error":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<a href="/client-error-body">must not be parsed</a>`)
		case "/server-error":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `<a href="/server-error-body">must not be parsed</a>`)
		case "/other":
			w.WriteHeader(600)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 1, 20, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	tests := []struct {
		path   string
		kind   crawler.ResultKind
		status string
	}{
		{path: "/", kind: crawler.ResultSuccess, status: "200 OK"},
		{path: "/ok", kind: crawler.ResultSuccess, status: "200 OK"},
		{path: "/empty", kind: crawler.ResultSuccess, status: "204 No Content"},
		{path: "/redirect", kind: crawler.ResultRedirect, status: "302 Found"},
		{path: "/client-error", kind: crawler.ResultHTTP4XX, status: "404 Not Found"},
		{path: "/server-error", kind: crawler.ResultHTTP5XX, status: "500 Internal Server Error"},
		{path: "/other", kind: crawler.ResultOtherHTTPStatus, status: "600 status code 600"},
	}
	for _, test := range tests {
		page := findPage(t, result, server.URL+test.path)
		if page.ResultKind != test.kind {
			t.Errorf("%s result = %s, want %s", test.path, page.ResultKind, test.kind)
		}
		if page.Status != test.status {
			t.Errorf("%s status = %q, want %q", test.path, page.Status, test.status)
		}
		if page.Error != "" {
			t.Errorf("%s error = %q, want empty for an HTTP response", test.path, page.Error)
		}
	}

	for _, forbiddenPath := range []string{
		"/must-not-follow",
		"/redirect-body",
		"/client-error-body",
		"/server-error-body",
	} {
		if countPath(requests.snapshot(), forbiddenPath) != 0 {
			t.Errorf("%s was requested: %v", forbiddenPath, requests.snapshot())
		}
		if _, exists := result.DepthByURL[server.URL+forbiddenPath]; exists {
			t.Errorf("%s was extracted from a non-success response", forbiddenPath)
		}
	}

	if len(result.Problems) != 2 {
		t.Fatalf("problems = %+v, want only HTTP 4xx and HTTP 5xx", result.Problems)
	}
	if result.Problems[0].Kind != crawler.ResultHTTP4XX ||
		result.Problems[1].Kind != crawler.ResultHTTP5XX {
		t.Fatalf("problem kinds = %s, %s; want HTTP_4XX, HTTP_5XX",
			result.Problems[0].Kind,
			result.Problems[1].Kind,
		)
	}
}

func TestHTTPStatusRangeBoundaries(t *testing.T) {
	tests := []struct {
		status int
		kind   crawler.ResultKind
	}{
		{status: 299, kind: crawler.ResultSuccess},
		{status: 300, kind: crawler.ResultRedirect},
		{status: 399, kind: crawler.ResultRedirect},
		{status: 400, kind: crawler.ResultHTTP4XX},
		{status: 499, kind: crawler.ResultHTTP4XX},
		{status: 500, kind: crawler.ResultHTTP5XX},
		{status: 599, kind: crawler.ResultHTTP5XX},
		{status: 600, kind: crawler.ResultOtherHTTPStatus},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()

			inspector := newTestInspector(t, server.URL, 0, 1, time.Second)
			page, err := inspector.InspectStart(context.Background())
			if err != nil {
				t.Fatalf("InspectStart: %v", err)
			}
			if page.ResultKind != test.kind {
				t.Fatalf("status %d result = %s, want %s", test.status, page.ResultKind, test.kind)
			}
		})
	}
}

func TestCrawlClassifiesNetworkErrorAndContinues(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			w.Header().Set("Connection", "close")
			writeTestHTML(w, `<a href="/network">Network failure</a>`, `<a href="/fast">Fast</a>`)
		case "/network":
			connection, _, hijackErr := w.(http.Hijacker).Hijack()
			if hijackErr == nil {
				connection.Close()
			}
		case "/fast":
			writeTestHTML(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/", 1, 10, time.Second)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl must continue after a network error: %v", err)
	}

	networkPage := findPage(t, result, server.URL+"/network")
	if networkPage.ResultKind != crawler.ResultNetworkError {
		t.Fatalf("network result = %s, want NETWORK_ERROR", networkPage.ResultKind)
	}
	if networkPage.Status != "" {
		t.Fatalf("network status = %q, want no HTTP status", networkPage.Status)
	}
	if networkPage.Error == "" {
		t.Fatal("network error message is empty")
	}
	if fastPage := findPage(t, result, server.URL+"/fast"); fastPage.ResultKind != crawler.ResultSuccess {
		t.Fatalf("fast result = %s, want SUCCESS", fastPage.ResultKind)
	}
	if len(result.Problems) != 1 || result.Problems[0].Kind != crawler.ResultNetworkError {
		t.Fatalf("problems = %+v, want one NETWORK_ERROR", result.Problems)
	}
}

func TestTimeoutWhileReadingBodyKeepsReceivedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL, 0, 1, 250*time.Millisecond)
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl must classify a body-read timeout without stopping: %v", err)
	}

	page := findPage(t, result, server.URL+"/")
	if page.ResultKind != crawler.ResultTimeout {
		t.Fatalf("result = %s, want TIMEOUT", page.ResultKind)
	}
	if page.Status != "200 OK" {
		t.Fatalf("status = %q, want the status received before the body timed out", page.Status)
	}
	if page.Error == "" {
		t.Fatal("body-read timeout error message is empty")
	}
	if len(result.Problems) != 1 || result.Problems[0].Status != "200 OK" {
		t.Fatalf("problem = %+v, want timeout with the received HTTP status", result.Problems)
	}
}

func TestProblemKeepsAllDistinctSources(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w, `<a href="/a">A</a>`, `<a href="/b">B</a>`)
		case "/a":
			writeTestHTML(w, `<a href="/broken">Broken</a>`, `<a href="/broken#again">Broken again</a>`)
		case "/b":
			writeTestHTML(w, `<a href="/broken">Broken</a>`)
		case "/broken":
			http.NotFound(w, r)
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

	if countPath(requests.snapshot(), "/broken") != 1 {
		t.Fatalf("broken URL must be requested once: %v", requests.snapshot())
	}
	if len(result.Problems) != 1 {
		t.Fatalf("problems = %+v, want one unique broken URL", result.Problems)
	}
	problem := result.Problems[0]
	if problem.URL != server.URL+"/broken" || problem.Kind != crawler.ResultHTTP4XX {
		t.Fatalf("problem = %+v, want the broken 404 URL", problem)
	}
	if problem.Status != "404 Not Found" || problem.Error != "" {
		t.Fatalf("problem status/error = %q/%q, want HTTP status only", problem.Status, problem.Error)
	}
	wantSources := []string{server.URL + "/a", server.URL + "/b"}
	if !reflect.DeepEqual(problem.Sources, wantSources) {
		t.Fatalf("problem sources = %v, want %v", problem.Sources, wantSources)
	}
}
