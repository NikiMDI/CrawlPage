package crawler

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrEmptyURL          = errors.New("URL is empty")
	ErrUnsupportedScheme = errors.New("unsupported URL scheme")
	ErrMissingHost       = errors.New("URL has no host")
	ErrInvalidPort       = errors.New("invalid URL port")
)

func normalizeStartURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, ErrEmptyURL
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", rawURL, err)
	}
	if !parsed.IsAbs() {
		return nil, fmt.Errorf("%w: start URL must be absolute", ErrMissingHost)
	}
	return canonicalHTTPURL(parsed)
}

func normalizeURL(base *url.URL, rawHref string) (*url.URL, error) {
	if base == nil {
		return nil, fmt.Errorf("base URL is nil")
	}

	trimmed := strings.TrimSpace(rawHref)
	if trimmed == "" {
		return canonicalHTTPURL(base)
	}

	reference, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", rawHref, err)
	}
	if reference.Scheme != "" && !isHTTPScheme(reference.Scheme) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedScheme, reference.Scheme)
	}

	resolved := base.ResolveReference(reference)
	return canonicalHTTPURL(resolved)
}

func canonicalHTTPURL(input *url.URL) (*url.URL, error) {
	if input == nil {
		return nil, ErrEmptyURL
	}

	normalized := *input
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	if !isHTTPScheme(normalized.Scheme) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedScheme, normalized.Scheme)
	}

	hostname := strings.ToLower(normalized.Hostname())
	if hostname == "" {
		return nil, ErrMissingHost
	}

	port, err := normalizedPort(normalized.Scheme, normalized.Port())
	if err != nil {
		return nil, err
	}
	normalized.Host = normalizedHost(hostname, port)

	if normalized.Path == "" {
		normalized.Path = "/"
	}
	normalized.Fragment = ""
	normalized.RawFragment = ""

	return &normalized, nil
}

func normalizedHost(hostname, port string) string {
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

func normalizedPort(scheme, rawPort string) (string, error) {
	if rawPort == "" {
		return "", nil
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%w: %q", ErrInvalidPort, rawPort)
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return "", nil
	}
	return strconv.Itoa(port), nil
}
