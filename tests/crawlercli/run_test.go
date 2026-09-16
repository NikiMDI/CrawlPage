package crawlercli_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"example.com/graph-test-site-go/internal/crawlercli"
)

func TestRunPrintsStageFourDFSReport(t *testing.T) {
	var requestsMu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.URL.Path)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="/a">A</a><a href="/b">B</a>`)
		default:
			fmt.Fprint(w, `<h1>Page</h1>`)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/",
			"--depth", "2",
			"--max-pages", "2",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)

	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	requestsMu.Lock()
	gotRequests := append([]string(nil), requests...)
	requestsMu.Unlock()
	if wantRequests := []string{"/", "/a"}; !reflect.DeepEqual(gotRequests, wantRequests) {
		t.Fatalf("request order = %v, want DFS prefix %v", gotRequests, wantRequests)
	}
	for _, expected := range []string{
		"Stage 4: redirect chains",
		"Maximum depth:      2",
		"Maximum pages:      2",
		"Maximum redirects:  10",
		"Max pages reached:  true",
		"Pages checked:      2",
		"Links discovered:   2",
		"Successful:         2",
		"Broken links:       0",
		"Level 2 — details",
		"DFS visit order",
		"PAGE 1",
		"Depth:  0",
		server.URL + "/a",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunRejectsInvalidStageFourConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing URL", args: nil},
		{name: "negative depth", args: []string{"--url", "http://example.com", "--depth", "-1"}},
		{name: "zero pages", args: []string{"--url", "http://example.com", "--max-pages", "0"}},
		{name: "zero timeout", args: []string{"--url", "http://example.com", "--timeout", "0s"}},
		{name: "negative redirects", args: []string{"--url", "http://example.com", "--max-redirects", "-1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := crawlercli.Run(context.Background(), test.args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("Run exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr.String(), "configuration error") {
				t.Fatalf("stderr = %q, want configuration error", stderr.String())
			}
		})
	}
}

func TestRunPrintsHTTPResultSummaryAndProblemSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w,
				`<a href="/ok">OK</a>`,
				`<a href="/redirect">Redirect</a>`,
				`<a href="/missing">Missing</a>`,
				`<a href="/error">Error</a>`,
			)
		case "/ok":
			fmt.Fprint(w, `<h1>OK</h1>`)
		case "/redirect":
			w.Header().Set("Location", "/ok")
			w.WriteHeader(http.StatusFound)
		case "/missing":
			http.NotFound(w, r)
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/",
			"--depth", "1",
			"--max-pages", "10",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Pages checked:      5",
		"Successful:         3",
		"Redirect chains:    1",
		"Redirect hops:      1",
		"Broken links:       2",
		"HTTP 4xx:           1",
		"HTTP 5xx:           1",
		"Timeouts:           0",
		"Network errors:     0",
		"Result: HTTP_4XX\nStatus: 404 Not Found",
		"Result: HTTP_5XX\nStatus: 500 Internal Server Error",
		"Broken link details",
		"REDIRECT 1",
		server.URL + "/redirect — 302 Found -> " + server.URL + "/ok",
		server.URL + "/ok — 200 OK",
		"Final result: SUCCESS",
		"Found on:\n- " + server.URL + "/",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunPrintsTimeoutWithoutHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL,
			"--depth", "0",
			"--max-pages", "1",
			"--timeout", "50ms",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for _, expected := range []string{
		"Broken links:       1",
		"Timeouts:           1",
		"Network errors:     0",
		"Result: TIMEOUT",
		"context deadline exceeded",
		"Found on:\n- [start URL]",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunReportsActualFetchDepth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="/long">Long</a><a href="/short">Short</a>`)
		case "/long":
			fmt.Fprint(w, `<a href="/middle">Middle</a>`)
		case "/middle":
			fmt.Fprint(w, `<a href="/target">Target</a>`)
		case "/short":
			fmt.Fprint(w, `<a href="/target">Target</a>`)
		default:
			fmt.Fprint(w, `<h1>Page</h1>`)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/",
			"--depth", "3",
			"--max-pages", "10",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if strings.Contains(stdout.String(), "Deepest checked:") {
		t.Errorf("report contains the removed deepest-depth summary field:\n%s", stdout.String())
	}
	targetPage := "Depth:  2\nFetched at depth: 3\nURL:    " + server.URL + "/target"
	if !strings.Contains(stdout.String(), targetPage) {
		t.Errorf("target page depths are not reported together:\n%s", stdout.String())
	}
}

func TestRunPrintsRedirectCycleMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusFound)
		case "/b":
			w.Header().Set("Location", "/a")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/a",
			"--depth", "0",
			"--max-pages", "2",
			"--max-redirects", "10",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Redirect chains:    1",
		"Redirect hops:      2",
		"Redirect errors:    1",
		"Broken links:       1",
		server.URL + "/a — CYCLE (already requested in this chain)",
		"Final result: REDIRECT_ERROR",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunCountsEveryUniqueRedirectURLAsChecked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/r1":
			w.Header().Set("Location", "/r2")
			w.WriteHeader(http.StatusFound)
		case "/r2":
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/final":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<h1>Final</h1>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/r1",
			"--depth", "0",
			"--max-pages", "3",
			"--max-redirects", "10",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Max pages reached:  false",
		"Pages checked:      3",
		"Redirect chains:    1",
		"Redirect hops:      2",
		"Final result: SUCCESS",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunReportsPageLimitInsideRedirectChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/r1":
			w.Header().Set("Location", "/r2")
			w.WriteHeader(http.StatusFound)
		case "/r2":
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
		case "/final":
			fmt.Fprint(w, `<h1>Must not be requested</h1>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/r1",
			"--depth", "0",
			"--max-pages", "2",
			"--max-redirects", "10",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Max pages reached:  true",
		"Pages checked:      2",
		"Stopped by max-pages: 1",
		"Broken links:       0",
		"Result: PAGE_LIMIT",
		server.URL + "/final — NOT REQUESTED (max-pages reached)",
		"Final result: PAGE_LIMIT",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunCountsDirectTargetAfterUnfollowedRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="/old">Old</a><a href="/target">Target</a>`)
		case "/old":
			w.Header().Set("Location", "/target")
			w.WriteHeader(http.StatusFound)
		case "/target":
			fmt.Fprint(w, `<a href="/leaf">Leaf</a>`)
		case "/leaf":
			fmt.Fprint(w, `<h1>Leaf</h1>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := crawlercli.Run(
		context.Background(),
		[]string{
			"--url", server.URL + "/",
			"--depth", "2",
			"--max-pages", "10",
			"--max-redirects", "0",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Links discovered:   3",
		"Unique HTTP links:  3",
		"Internal links:     3",
		"Redirect errors:    1",
		"Broken links:       1",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}
