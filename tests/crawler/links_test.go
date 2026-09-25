package crawler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestInspectStartReadsOnlyAnchorHrefs(t *testing.T) {
	markup := `<!doctype html>
<html><body>
  <a class="first" href="/one">One</a>
  <A HREF='../two'>Two</A>
  <a href=/three>Three</a>
  <a>No href</a>
  <script>const fake = '<a href="/script">';</script>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		fmt.Fprint(w, markup)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/docs/index.html", 0, 1, time.Second)
	page, err := inspector.InspectStart(context.Background())
	if err != nil {
		t.Fatalf("InspectStart: %v", err)
	}

	got := make([]string, 0, len(page.Links))
	for _, link := range page.Links {
		got = append(got, link.RawHref)
	}
	want := []string{"/one", "../two", "/three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anchor hrefs = %v, want %v", got, want)
	}
}

func TestInspectStartResolvesLinksAgainstFirstBaseHref(t *testing.T) {
	markup := `<!doctype html>
<html><head>
  <base href="/materials/">
  <base href="/ignored/">
</head><body>
  <a href="before.html">Before</a>
  <a href="go.html">Go</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		fmt.Fprint(w, markup)
	}))
	defer server.Close()

	inspector := newTestInspector(t, server.URL+"/docs/index.html", 0, 1, time.Second)
	page, err := inspector.InspectStart(context.Background())
	if err != nil {
		t.Fatalf("InspectStart: %v", err)
	}

	got := make([]string, 0, len(page.Links))
	for _, link := range page.Links {
		got = append(got, link.URL)
	}
	want := []string{
		server.URL + "/materials/before.html",
		server.URL + "/materials/go.html",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links resolved with <base> = %v, want %v", got, want)
	}
	for _, link := range page.Links {
		if link.SourceURL != server.URL+"/docs/index.html" {
			t.Fatalf("source URL = %q, want the HTML document URL", link.SourceURL)
		}
	}
}
