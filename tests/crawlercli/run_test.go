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

func TestRunPrintsStageTwoDFSReport(t *testing.T) {
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
		"Stage 2: sequential depth-first crawl",
		"Maximum depth:      2",
		"Maximum pages:      2",
		"Max pages reached:  true",
		"Pages checked:      2",
		"Links discovered:   2",
		"Level 2 — DFS visit order",
		"PAGE 1",
		"Depth:  0",
		server.URL + "/a",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunRejectsInvalidStageTwoConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing URL", args: nil},
		{name: "negative depth", args: []string{"--url", "http://example.com", "--depth", "-1"}},
		{name: "zero pages", args: []string{"--url", "http://example.com", "--max-pages", "0"}},
		{name: "zero timeout", args: []string{"--url", "http://example.com", "--timeout", "0s"}},
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
	if !strings.Contains(stdout.String(), "Deepest checked:    3") {
		t.Errorf("report does not contain actual deepest fetch depth:\n%s", stdout.String())
	}
	targetPage := "Depth:  2\nFetched at depth: 3\nURL:    " + server.URL + "/target"
	if !strings.Contains(stdout.String(), targetPage) {
		t.Errorf("target page depths are not reported together:\n%s", stdout.String())
	}
}
