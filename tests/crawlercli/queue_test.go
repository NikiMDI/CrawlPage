package crawlercli_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/graph-test-site-go/internal/crawlercli"
)

func TestRunRejectsNonPositiveMaximumQueue(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := crawlercli.Run(
				context.Background(),
				[]string{"--url", "http://example.com", "--max-queue", value},
				&stdout,
				&stderr,
			)

			if exitCode != 2 {
				t.Fatalf("Run exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr.String(), "maximum queue size must be positive") {
				t.Fatalf("stderr = %q, want maximum-queue validation error", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want no report for invalid configuration", stdout.String())
			}
		})
	}
}

func TestRunReportsQueueLimitAndOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<a href="/a">A</a><a href="/b">B</a>`)
			return
		}
		fmt.Fprint(w, `<h1>Page</h1>`)
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
			"--max-queue", "1",
			"--timeout", "1s",
		},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("Run exit code = %d, stderr = %q", exitCode, stderr.String())
	}

	for _, expected := range []string{
		"Maximum queue:      1",
		"Peak queue:         1",
		"Queue limit reached: true",
		"Skipped by queue:   1",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("report does not contain %q:\n%s", expected, stdout.String())
		}
	}
}
