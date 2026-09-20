package crawler

import (
	"net/url"
	"strconv"
	"strings"
)

type scope struct {
	scheme   string
	hostname string
	port     int
}

func newScope(startURL *url.URL) scope {
	return scope{
		scheme:   strings.ToLower(startURL.Scheme),
		hostname: strings.ToLower(startURL.Hostname()),
		port:     effectivePort(startURL),
	}
}

func (s scope) contains(target *url.URL) bool {
	if target == nil || !strings.EqualFold(target.Hostname(), s.hostname) {
		return false
	}

	targetScheme := strings.ToLower(target.Scheme)
	targetPort := effectivePort(target)
	if targetScheme == s.scheme {
		return targetPort == s.port
	}

	return s.scheme == "http" && targetScheme == "https" &&
		((s.port == 80 && targetPort == 443) || targetPort == s.port)
}

func effectivePort(target *url.URL) int {
	if port := target.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err == nil {
			return value
		}
	}
	if strings.EqualFold(target.Scheme, "https") {
		return 443
	}
	return 80
}
