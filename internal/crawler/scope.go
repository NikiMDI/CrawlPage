package crawler

import (
	"net/url"
	"strings"
)

type scope struct {
	hostname string
}

func newScope(startURL *url.URL) scope {
	return scope{
		hostname: strings.ToLower(startURL.Hostname()),
	}
}

func (s scope) contains(target *url.URL) bool {
	return target != nil &&
		isHTTPScheme(target.Scheme) &&
		strings.EqualFold(target.Hostname(), s.hostname)
}
