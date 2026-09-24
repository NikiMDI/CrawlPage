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
	"time"

	"example.com/graph-test-site-go/internal/crawlercli"
)

func TestRunPrintsFinalDFSReport(t *testing.T) {
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
			"--concurrency", "1",
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
	if wantRequests := []string{"/", "/a", "/b"}; !reflect.DeepEqual(gotRequests, wantRequests) {
		t.Fatalf("request order = %v, want DFS prefix %v", gotRequests, wantRequests)
	}
	for _, expected := range []string{
		"Maximum depth:      2",
		"Maximum pages:      2",
		"Maximum redirects:  10",
		"Concurrency:        1",
		"Maximum HTML bytes: 2097152",
		"Elapsed:",
		"Max pages reached:  true",
		"Pages checked:      2",
		"Links discovered:   2",
		"Successful:         2",
		"Broken links:       0",
		"Level 2 — details",
		"DFS-priority scheduling order",
		"PAGE 1",
		"Depth:  0",
		server.URL + "/a",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing URL", args: nil},
		{name: "negative depth", args: []string{"--url", "http://example.com", "--depth", "-1"}},
		{name: "zero pages", args: []string{"--url", "http://example.com", "--max-pages", "0"}},
		{name: "zero timeout", args: []string{"--url", "http://example.com", "--timeout", "0s"}},
		{name: "negative redirects", args: []string{"--url", "http://example.com", "--max-redirects", "-1"}},
		{name: "zero concurrency", args: []string{"--url", "http://example.com", "--concurrency", "0"}},
		{name: "negative concurrency", args: []string{"--url", "http://example.com", "--concurrency", "-1"}},
		{name: "negative HTML limit", args: []string{"--url", "http://example.com", "--max-html-bytes", "-1"}},
		{name: "zero HTML limit", args: []string{"--url", "http://example.com", "--max-html-bytes", "0"}},
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

func TestRunReturns130WhenContextIsCancelled(t *testing.T) {
	requestStarted := make(chan struct{})
	requestStopped := make(chan struct{})
	var startOnce sync.Once
	var stopOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(requestStarted) })
		<-r.Context().Done()
		stopOnce.Do(func() { close(requestStopped) })
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runOutcome struct {
		exitCode int
		stdout   string
		stderr   string
	}
	finished := make(chan runOutcome, 1)
	go func() {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := crawlercli.Run(
			ctx,
			[]string{
				"--url", server.URL,
				"--depth", "3",
				"--max-pages", "100",
				"--concurrency", "4",
				"--timeout", "5s",
			},
			&stdout,
			&stderr,
		)
		finished <- runOutcome{
			exitCode: exitCode,
			stdout:   stdout.String(),
			stderr:   stderr.String(),
		}
	}()

	select {
	case <-requestStarted:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("CLI request did not start")
	}

	var outcome runOutcome
	select {
	case outcome = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("CLI did not stop after cancellation")
	}
	select {
	case <-requestStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("CLI HTTP request remained active after cancellation")
	}

	if outcome.exitCode != 130 {
		t.Fatalf("Run exit code = %d, want 130", outcome.exitCode)
	}
	if outcome.stderr != "crawl cancelled\n" {
		t.Fatalf("stderr = %q, want cancellation message", outcome.stderr)
	}
	if outcome.stdout != "" {
		t.Fatalf("cancelled crawl printed a partial report:\n%s", outcome.stdout)
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
			"--concurrency", "1",
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
		"HTTP 404:           1",
		"HTTP 5xx:           1",
		"HTTP 500:           1",
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

func TestRunPrintsSpecificHTTPStatusCountsInOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w,
				`<a href="/status-503">503</a>`,
				`<a href="/status-413-a">413 A</a>`,
				`<a href="/status-404-a">404 A</a>`,
				`<a href="/status-500">500</a>`,
				`<a href="/status-413-b">413 B</a>`,
				`<a href="/status-404-b">404 B</a>`,
			)
		case "/status-404-a", "/status-404-b":
			w.WriteHeader(http.StatusNotFound)
		case "/status-413-a", "/status-413-b":
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case "/status-500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/status-503":
			w.WriteHeader(http.StatusServiceUnavailable)
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
			"--max-pages", "20",
			"--concurrency", "4",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	report := stdout.String()
	wantInOrder := []string{
		"Broken links:       6",
		"HTTP 4xx:           4",
		"HTTP 404:           2",
		"HTTP 413:           2",
		"HTTP 5xx:           2",
		"HTTP 500:           1",
		"HTTP 503:           1",
		"Timeouts:           0",
	}
	lastPosition := -1
	for _, expected := range wantInOrder {
		position := strings.Index(report, expected)
		if position < 0 {
			t.Fatalf("report does not contain %q:\n%s", expected, report)
		}
		if position <= lastPosition {
			t.Fatalf("%q is out of order in report:\n%s", expected, report)
		}
		lastPosition = position
	}
}

func TestRunReportsHTMLBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/must-not-be-crawled">hidden</a>`+strings.Repeat("x", 128))
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
			"--max-html-bytes", "32",
			"--concurrency", "1",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for _, expected := range []string{
		"Maximum HTML bytes: 32",
		"Pages checked:      1",
		"Broken links:       0",
		"HTML too large:     1",
		"Result: HTML_TOO_LARGE",
		"Broken link details\nNo broken links were found.",
		"HTML too large details",
		"HTML_TOO_LARGE 1",
		"Status: 200 OK",
		"exceeds limit of 32 bytes",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "PROBLEM 1\nURL:") {
		t.Errorf("oversized HTML was incorrectly printed as a broken link:\n%s", stdout.String())
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
			"--concurrency", "1",
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
			"--concurrency", "1",
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
			"--concurrency", "1",
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
			"--concurrency", "1",
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
			"--concurrency", "1",
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
		server.URL + "/final — NOT PROCESSED (max-pages reached after response headers)",
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
			"--concurrency", "1",
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
