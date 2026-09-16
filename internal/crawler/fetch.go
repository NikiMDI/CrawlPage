package crawler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type fetchedURL struct {
	ResultKind  ResultKind
	Status      string
	Error       string
	ContentType string
	Location    string
	Hrefs       []string
}

type crawlSession struct {
	maxPages        int
	pagesChecked    int
	maxPagesReached bool
	cache           map[string]fetchedURL
}

func newCrawlSession(maxPages int) *crawlSession {
	return &crawlSession{
		maxPages: maxPages,
		cache:    make(map[string]fetchedURL),
	}
}

func (s *crawlSession) fetch(
	ctx context.Context,
	inspector *Inspector,
	target *url.URL,
) (fetchedURL, bool, error) {
	key := target.String()
	if cached, exists := s.cache[key]; exists {
		return cached, true, nil
	}
	if s.pagesChecked >= s.maxPages {
		s.maxPagesReached = true
		return fetchedURL{}, false, nil
	}

	s.pagesChecked++
	fetched, err := inspector.fetchURL(ctx, target)
	if err != nil {
		return fetchedURL{}, true, err
	}
	s.cache[key] = fetched
	return fetched, true, nil
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

	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return fetchedURL{}, parentErr
		}
		result.ResultKind = classifyRequestError(readErr)
		result.Error = fmt.Sprintf("read %s: %v", target, readErr)
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
