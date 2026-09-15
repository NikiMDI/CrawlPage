package crawler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type LinkKind string

const (
	LinkInternal LinkKind = "INTERNAL"
	LinkExternal LinkKind = "EXTERNAL"
)

type DiscoveredLink struct {
	SourceURL string
	RawHref   string
	URL       string
	Kind      LinkKind
}

type SkippedLink struct {
	SourceURL string
	RawHref   string
	Reason    string
}

type PageResult struct {
	URL             string
	Depth           int
	FetchDepth      int
	StatusCode      int
	Status          string
	ContentType     string
	LinksDiscovered int
	Links           []DiscoveredLink
	Skipped         []SkippedLink
	FetchError      error
}

type CrawlResult struct {
	StartURL        string
	MaxDepth        int
	MaxPages        int
	Pages           []PageResult
	DepthByURL      map[string]int
	SourcesByURL    map[string][]string
	LinksDiscovered int
	MaxPagesReached bool
}

type Inspector struct {
	config   Config
	startURL *url.URL
	scope    scope
	client   *http.Client
}

func New(config Config) (*Inspector, error) {
	startURL, err := validateConfig(config)
	if err != nil {
		return nil, err
	}

	return &Inspector{
		config:   config,
		startURL: startURL,
		scope:    newScope(startURL),
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (i *Inspector) InspectStart(ctx context.Context) (PageResult, error) {
	return i.inspectURL(ctx, i.startURL, 0)
}

func (i *Inspector) inspectURL(ctx context.Context, target *url.URL, depth int) (PageResult, error) {
	result := PageResult{
		URL:        target.String(),
		Depth:      depth,
		FetchDepth: depth,
	}

	requestContext, cancel := context.WithTimeout(ctx, i.config.RequestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		target.String(),
		nil,
	)
	if err != nil {
		return result, fmt.Errorf("create request: %w", err)
	}

	response, err := i.client.Do(request)
	if err != nil {
		return result, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer response.Body.Close()

	result.StatusCode = response.StatusCode
	result.Status = response.Status
	result.ContentType = response.Header.Get("Content-Type")

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, nil
	}
	if !isHTMLContentType(result.ContentType) {
		return result, nil
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return result, fmt.Errorf("read %s: %w", target, err)
	}

	hrefs, err := extractHrefs(bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("parse HTML from %s: %w", target, err)
	}
	result.LinksDiscovered = len(hrefs)

	for _, rawHref := range hrefs {
		linkTarget, normalizeErr := normalizeURL(target, rawHref)
		if normalizeErr != nil {
			result.Skipped = append(result.Skipped, SkippedLink{
				SourceURL: result.URL,
				RawHref:   rawHref,
				Reason:    normalizeErr.Error(),
			})
			continue
		}

		kind := LinkExternal
		if i.scope.contains(linkTarget) {
			kind = LinkInternal
		}
		result.Links = append(result.Links, DiscoveredLink{
			SourceURL: result.URL,
			RawHref:   rawHref,
			URL:       linkTarget.String(),
			Kind:      kind,
		})
	}

	return result, nil
}

func isHTMLContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "text/html") ||
		strings.EqualFold(mediaType, "application/xhtml+xml")
}
