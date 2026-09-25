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

func TestFullQueueAndPageBudgetUseContentTypeNotURLSuffix(t *testing.T) {
	var requests requestRecorder
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.URL.Path)
		switch r.URL.Path {
		case "/":
			writeTestHTML(w,
				`<a href="/real.pdf">PDF data</a>`,
				`<a href="/actually-html.PDF">HTML at PDF URL</a>`,
				`<a href="/asset">Extensionless binary</a>`,
				`<a href="/picture.html">Image at HTML URL</a>`,
			)
		case "/actually-html.PDF":
			writeTestHTML(w, `<a href="/nested">Nested HTML</a>`)
		case "/real.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, `<a href="/fake-from-pdf">Must not parse</a>`)
		case "/asset":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, `<a href="/fake-from-asset">Must not parse</a>`)
		case "/picture.html":
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprint(w, `<a href="/fake-from-image">Must not parse</a>`)
		default:
			writeTestHTML(w)
		}
	}))
	defer server.Close()

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/",
		RequestTimeout: time.Second,
		MaxDepth:       2,
		MaxPages:       2,
		MaxRedirects:   10,
		Concurrency:    1,
		MaxQueue:       1,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	result, err := inspector.Crawl(context.Background())
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	wantRequests := []string{"/", "/real.pdf", "/actually-html.PDF", "/nested", "/asset", "/picture.html"}
	if got := requests.snapshot(); !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("requests = %v, want %v", got, wantRequests)
	}
	if result.PagesChecked != 2 || !result.MaxPagesReached {
		t.Fatalf("pages checked/max reached = %d/%t, want 2/true", result.PagesChecked, result.MaxPagesReached)
	}
	if result.PeakQueueSize != 1 || !result.QueueLimitReached {
		t.Fatalf("peak queue/pressure = %d/%t, want 1/true", result.PeakQueueSize, result.QueueLimitReached)
	}
	if got := len(result.Pages); got != 5 {
		t.Fatalf("result count = %d, want root, one HTML page, and three non-HTML results", got)
	}
	htmlAtPDF := findPage(t, result, server.URL+"/actually-html.PDF")
	if !htmlAtPDF.HTMLParsed || htmlAtPDF.ResultKind != crawler.ResultSuccess {
		t.Fatalf(".PDF URL returning text/html was not parsed: %+v", htmlAtPDF)
	}
	for _, path := range []string{"/real.pdf", "/asset", "/picture.html"} {
		page := findPage(t, result, server.URL+path)
		if page.HTMLParsed || page.ResultKind != crawler.ResultSuccess {
			t.Errorf("%s should be successful non-HTML: %+v", path, page)
		}
	}
	for _, path := range []string{"/fake-from-pdf", "/fake-from-asset", "/fake-from-image"} {
		if _, found := result.SourcesByURL[server.URL+path]; found {
			t.Errorf("non-HTML body incorrectly parsed: %s", path)
		}
	}
	if got := result.SourcesByURL[server.URL+"/nested"]; !reflect.DeepEqual(got, []string{server.URL + "/actually-html.PDF"}) {
		t.Errorf("nested source = %v, want .PDF HTML page", got)
	}
}

func TestPDFContentTypeDoesNotWaitForResponseBody(t *testing.T) {
	releaseBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-releaseBody:
			fmt.Fprint(w, `<a href="/fake-link">Not HTML</a>`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(releaseBody)

	inspector, err := crawler.New(crawler.Config{
		StartURL:       server.URL + "/document.PDF",
		RequestTimeout: 2 * time.Second,
		MaxDepth:       1,
		MaxPages:       1,
		MaxRedirects:   10,
		Concurrency:    1,
		MaxQueue:       1,
	})
	if err != nil {
		t.Fatalf("crawler.New: %v", err)
	}
	finished := make(chan struct {
		result crawler.CrawlResult
		err    error
	}, 1)
	go func() {
		result, crawlErr := inspector.Crawl(context.Background())
		finished <- struct {
			result crawler.CrawlResult
			err    error
		}{result: result, err: crawlErr}
	}()

	select {
	case outcome := <-finished:
		if outcome.err != nil {
			t.Fatalf("Crawl: %v", outcome.err)
		}
		if outcome.result.PagesChecked != 0 || len(outcome.result.Pages) != 1 {
			t.Fatalf("PDF result/pages checked = %d/%d, want 1/0", len(outcome.result.Pages), outcome.result.PagesChecked)
		}
		page := outcome.result.Pages[0]
		if page.ResultKind != crawler.ResultSuccess || page.HTMLParsed || page.ContentType != "application/pdf" {
			t.Fatalf("non-HTML result = %+v", page)
		}
	case <-time.After(time.Second):
		t.Fatal("crawler waited for the PDF body after receiving Content-Type headers")
	}
}
