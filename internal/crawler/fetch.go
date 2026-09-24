package crawler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

type fetchedURL struct {
	ResultKind  ResultKind
	Status      string
	Error       string
	ContentType string
	Location    string
	BaseHref    *string
	Hrefs       []string
}

type fetchEntry struct {
	ready     chan struct{}
	result    fetchedURL
	available bool
	err       error
}

type crawlSession struct {
	mu              sync.Mutex
	maxPages        int
	pagesChecked    int
	maxPagesReached bool
	cache           map[string]*fetchEntry
}

func newCrawlSession(maxPages int) *crawlSession {
	return &crawlSession{
		maxPages: maxPages,
		cache:    make(map[string]*fetchEntry),
	}
}

func (s *crawlSession) fetch(
	ctx context.Context,
	inspector *Inspector,
	target *url.URL,
) (fetchedURL, bool, error) {
	if err := ctx.Err(); err != nil {
		return fetchedURL{}, false, err
	}
	key := target.String()

	s.mu.Lock()
	if entry, exists := s.cache[key]; exists {
		s.mu.Unlock()
		select {
		case <-entry.ready:
			return entry.result, entry.available, entry.err
		case <-ctx.Done():
			return fetchedURL{}, true, ctx.Err()
		}
	}

	entry := &fetchEntry{ready: make(chan struct{})}
	s.cache[key] = entry
	s.mu.Unlock()

	fetched, available, err := inspector.fetchURL(ctx, target, s.admit)

	s.mu.Lock()
	entry.result = fetched
	entry.available = available
	entry.err = err
	close(entry.ready)
	s.mu.Unlock()

	return fetched, available, err
}

func (s *crawlSession) admit(result fetchedURL) bool {
	// A successful non-HTML resource is still checked for its HTTP status, but it
	// is not a crawlable page and therefore does not spend the page budget.
	if !result.consumesPageBudget() {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pagesChecked >= s.maxPages {
		s.maxPagesReached = true
		return false
	}
	s.pagesChecked++
	return true
}

func (f fetchedURL) consumesPageBudget() bool {
	// Redirects and failures remain part of the budget because they are exactly
	// the page/link outcomes the crawler must diagnose and report.
	return f.ResultKind != ResultSuccess || isHTMLContentType(f.ContentType)
}

func (s *crawlSession) stats() (pagesChecked int, maxPagesReached bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pagesChecked, s.maxPagesReached
}

func (i *Inspector) fetchURL(
	ctx context.Context,
	target *url.URL,
	admit func(fetchedURL) bool,
) (fetchedURL, bool, error) {
	response, cancelRequest, requestErr := i.doRequest(ctx, target)
	if requestErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return fetchedURL{}, false, parentErr
		}
		result := fetchedURL{
			ResultKind: classifyRequestError(requestErr),
			Error:      requestErr.Error(),
		}
		return result, admit(result), nil
	}
	defer cancelRequest()
	defer response.Body.Close()

	result := fetchedURL{
		ResultKind:  classifyHTTPStatus(response.StatusCode),
		Status:      response.Status,
		ContentType: response.Header.Get("Content-Type"),
	}
	if !admit(result) {
		return result, false, nil
	}
	if result.ResultKind == ResultRedirect {
		result.Location = response.Header.Get("Location")
		return result, true, nil
	}
	if result.ResultKind != ResultSuccess || !isHTMLContentType(result.ContentType) {
		return result, true, nil
	}
	if response.ContentLength > i.config.MaxHTMLBytes {
		result.ResultKind = ResultHTMLTooLarge
		result.Error = fmt.Sprintf(
			"HTML response from %s exceeds limit of %d bytes",
			target,
			i.config.MaxHTMLBytes,
		)
		return result, true, nil
	}

	body, readErr := io.ReadAll(io.LimitReader(
		response.Body,
		i.config.MaxHTMLBytes+1,
	))
	if readErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return fetchedURL{}, false, parentErr
		}
		result.ResultKind = classifyRequestError(readErr)
		result.Error = fmt.Sprintf("read %s: %v", target, readErr)
		return result, true, nil
	}
	if int64(len(body)) > i.config.MaxHTMLBytes {
		result.ResultKind = ResultHTMLTooLarge
		result.Error = fmt.Sprintf(
			"HTML response from %s exceeds limit of %d bytes",
			target,
			i.config.MaxHTMLBytes,
		)
		return result, true, nil
	}

	links, parseErr := extractLinks(bytes.NewReader(body))
	if parseErr != nil {
		return fetchedURL{}, false, fmt.Errorf("parse HTML from %s: %w", target, parseErr)
	}
	result.BaseHref = links.BaseHref
	result.Hrefs = links.Hrefs
	return result, true, nil
}

func (i *Inspector) doRequest(
	ctx context.Context,
	target *url.URL,
) (*http.Response, context.CancelFunc, error) {
	requestContext, cancel := context.WithTimeout(ctx, i.config.RequestTimeout)
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		target.String(),
		nil,
	)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create request for %s: %w", target, err)
	}

	response, err := i.transport.RoundTrip(request)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return response, cancel, nil
}
