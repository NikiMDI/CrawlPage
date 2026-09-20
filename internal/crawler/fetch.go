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
	Hrefs       []string
}

type fetchEntry struct {
	ready  chan struct{}
	result fetchedURL
	err    error
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
			return entry.result, true, entry.err
		case <-ctx.Done():
			return fetchedURL{}, true, ctx.Err()
		}
	}
	if s.pagesChecked >= s.maxPages {
		s.maxPagesReached = true
		s.mu.Unlock()
		return fetchedURL{}, false, nil
	}

	entry := &fetchEntry{ready: make(chan struct{})}
	s.cache[key] = entry
	s.pagesChecked++
	s.mu.Unlock()

	fetched, err := inspector.fetchURL(ctx, target)

	s.mu.Lock()
	entry.result = fetched
	entry.err = err
	close(entry.ready)
	s.mu.Unlock()

	return fetched, true, err
}

func (s *crawlSession) stats() (pagesChecked int, maxPagesReached bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pagesChecked, s.maxPagesReached
}

func (s *crawlSession) hasFetchEntry(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.cache[target]
	return exists
}

func (i *Inspector) fetchURL(ctx context.Context, target *url.URL) (fetchedURL, error) {
	response, cancelRequest, requestErr := i.doRequest(ctx, target)
	if requestErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return fetchedURL{}, parentErr
		}
		return fetchedURL{
			ResultKind: classifyRequestError(requestErr),
			Error:      requestErr.Error(),
		}, nil
	}
	defer cancelRequest()
	defer response.Body.Close()

	result := fetchedURL{
		ResultKind:  classifyHTTPStatus(response.StatusCode),
		Status:      response.Status,
		ContentType: response.Header.Get("Content-Type"),
	}
	if result.ResultKind == ResultRedirect {
		result.Location = response.Header.Get("Location")
		return result, nil
	}
	if result.ResultKind != ResultSuccess || !isHTMLContentType(result.ContentType) {
		return result, nil
	}
	if response.ContentLength > i.config.MaxHTMLBytes {
		result.ResultKind = ResultHTMLTooLarge
		result.Error = fmt.Sprintf(
			"HTML response from %s exceeds limit of %d bytes",
			target,
			i.config.MaxHTMLBytes,
		)
		return result, nil
	}

	body, readErr := io.ReadAll(io.LimitReader(
		response.Body,
		i.config.MaxHTMLBytes+1,
	))
	if readErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return fetchedURL{}, parentErr
		}
		result.ResultKind = classifyRequestError(readErr)
		result.Error = fmt.Sprintf("read %s: %v", target, readErr)
		return result, nil
	}
	if int64(len(body)) > i.config.MaxHTMLBytes {
		result.ResultKind = ResultHTMLTooLarge
		result.Error = fmt.Sprintf(
			"HTML response from %s exceeds limit of %d bytes",
			target,
			i.config.MaxHTMLBytes,
		)
		return result, nil
	}

	hrefs, parseErr := extractHrefs(bytes.NewReader(body))
	if parseErr != nil {
		return fetchedURL{}, fmt.Errorf("parse HTML from %s: %w", target, parseErr)
	}
	result.Hrefs = hrefs
	return result, nil
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
