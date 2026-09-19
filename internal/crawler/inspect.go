package crawler

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
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

type RedirectHop struct {
	URL       string
	Status    string
	TargetURL string
}

type PageResult struct {
	URL                    string
	FinalURL               string
	Depth                  int
	FetchDepth             int
	ResultKind             ResultKind
	Status                 string
	Error                  string
	ContentType            string
	HTMLParsed             bool
	RedirectChain          []RedirectHop
	RedirectedOutsideScope bool
	RedirectCycleDetected  bool
	StoppedByPageLimit     bool
	LinksDiscovered        int
	Links                  []DiscoveredLink
	Skipped                []SkippedLink
}

type CrawlResult struct {
	StartURL        string
	MaxDepth        int
	MaxPages        int
	MaxRedirects    int
	Concurrency     int
	Elapsed         time.Duration
	PagesChecked    int
	Pages           []PageResult
	DepthByURL      map[string]int
	SourcesByURL    map[string][]string
	Problems        []Problem
	LinksDiscovered int
	MaxPagesReached bool
}

type Inspector struct {
	config    Config
	startURL  *url.URL
	scope     scope
	transport http.RoundTripper
}

func New(config Config) (*Inspector, error) {
	startURL, err := validateConfig(config)
	if err != nil {
		return nil, err
	}

	return &Inspector{
		config:    config,
		startURL:  startURL,
		scope:     newScope(startURL),
		transport: http.DefaultTransport,
	}, nil
}

func (i *Inspector) InspectStart(ctx context.Context) (PageResult, error) {
	result, _, err := i.inspectURL(
		ctx,
		i.startURL,
		0,
		newCrawlSession(i.config.MaxPages),
	)
	return result, err
}

func (i *Inspector) inspectURL(
	ctx context.Context,
	target *url.URL,
	depth int,
	session *crawlSession,
) (PageResult, bool, error) {
	result := PageResult{
		URL:        target.String(),
		FinalURL:   target.String(),
		Depth:      depth,
		FetchDepth: depth,
	}

	currentURL := target
	redirectURLs := map[string]struct{}{target.String(): {}}
	redirectsFollowed := 0
	pageChecked := false

	for {
		result.FinalURL = currentURL.String()
		fetched, available, fetchErr := session.fetch(ctx, i, currentURL)
		if fetchErr != nil {
			return result, pageChecked, fetchErr
		}
		if !available {
			result.ResultKind = ResultPageLimit
			result.Status = ""
			result.StoppedByPageLimit = true
			return result, pageChecked, nil
		}
		pageChecked = true

		if fetched.ResultKind == ResultRedirect {
			rawLocation := strings.TrimSpace(fetched.Location)
			result.Status = fetched.Status
			hop := RedirectHop{
				URL:    currentURL.String(),
				Status: fetched.Status,
			}

			if rawLocation == "" {
				result.RedirectChain = append(result.RedirectChain, hop)
				result.ResultKind = ResultRedirectError
				result.Error = fmt.Sprintf(
					"redirect response from %s has no Location header",
					currentURL,
				)
				return result, pageChecked, nil
			}

			nextURL, normalizeErr := normalizeURL(currentURL, rawLocation)
			if normalizeErr != nil {
				hop.TargetURL = rawLocation
				result.RedirectChain = append(result.RedirectChain, hop)
				result.ResultKind = ResultRedirectError
				result.Error = fmt.Sprintf(
					"resolve redirect Location %q from %s: %v",
					rawLocation,
					currentURL,
					normalizeErr,
				)
				return result, pageChecked, nil
			}

			hop.TargetURL = nextURL.String()
			result.RedirectChain = append(result.RedirectChain, hop)
			result.FinalURL = nextURL.String()

			if !i.scope.contains(nextURL) {
				result.ResultKind = ResultRedirect
				result.RedirectedOutsideScope = true
				return result, pageChecked, nil
			}
			if _, repeated := redirectURLs[nextURL.String()]; repeated {
				result.ResultKind = ResultRedirectError
				result.RedirectCycleDetected = true
				result.Error = fmt.Sprintf(
					"redirect cycle detected: %s already occurred in this chain",
					nextURL,
				)
				return result, pageChecked, nil
			}
			if redirectsFollowed >= i.config.MaxRedirects {
				result.ResultKind = ResultRedirectError
				result.Error = fmt.Sprintf(
					"maximum redirects exceeded: limit is %d; next URL is %s",
					i.config.MaxRedirects,
					nextURL,
				)
				return result, pageChecked, nil
			}

			redirectsFollowed++
			redirectURLs[nextURL.String()] = struct{}{}
			currentURL = nextURL
			result.Status = ""
			continue
		}

		result.FinalURL = currentURL.String()
		result.ResultKind = fetched.ResultKind
		result.Status = fetched.Status
		result.Error = fetched.Error
		result.ContentType = fetched.ContentType

		if result.ResultKind != ResultSuccess || !isHTMLContentType(result.ContentType) {
			return result, pageChecked, nil
		}
		result.HTMLParsed = true
		result.LinksDiscovered = len(fetched.Hrefs)

		for _, rawHref := range fetched.Hrefs {
			linkTarget, normalizeErr := normalizeURL(currentURL, rawHref)
			if normalizeErr != nil {
				result.Skipped = append(result.Skipped, SkippedLink{
					SourceURL: currentURL.String(),
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
				SourceURL: currentURL.String(),
				RawHref:   rawHref,
				URL:       linkTarget.String(),
				Kind:      kind,
			})
		}

		return result, pageChecked, nil
	}
}

func isHTMLContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "text/html") ||
		strings.EqualFold(mediaType, "application/xhtml+xml")
}
