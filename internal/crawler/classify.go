package crawler

import (
	"context"
	"errors"
	"net"
)

type ResultKind string

const (
	ResultSuccess         ResultKind = "SUCCESS"
	ResultRedirect        ResultKind = "REDIRECT"
	ResultRedirectError   ResultKind = "REDIRECT_ERROR"
	ResultHTTP4XX         ResultKind = "HTTP_4XX"
	ResultHTTP5XX         ResultKind = "HTTP_5XX"
	ResultTimeout         ResultKind = "TIMEOUT"
	ResultNetworkError    ResultKind = "NETWORK_ERROR"
	ResultOtherHTTPStatus ResultKind = "OTHER_HTTP_STATUS"
	ResultPageLimit       ResultKind = "PAGE_LIMIT"
)

func (kind ResultKind) IsBroken() bool {
	switch kind {
	case ResultRedirectError, ResultHTTP4XX, ResultHTTP5XX, ResultTimeout, ResultNetworkError:
		return true
	default:
		return false
	}
}

type Problem struct {
	URL     string
	Kind    ResultKind
	Status  string
	Error   string
	Sources []string
}

func classifyHTTPStatus(statusCode int) ResultKind {
	switch {
	case statusCode >= 200 && statusCode <= 299:
		return ResultSuccess
	case isRedirectStatus(statusCode):
		return ResultRedirect
	case statusCode >= 400 && statusCode <= 499:
		return ResultHTTP4XX
	case statusCode >= 500 && statusCode <= 599:
		return ResultHTTP5XX
	default:
		return ResultOtherHTTPStatus
	}
}

func isRedirectStatus(statusCode int) bool {
	switch statusCode {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

func classifyRequestError(err error) ResultKind {
	if errors.Is(err, context.DeadlineExceeded) {
		return ResultTimeout
	}

	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return ResultTimeout
	}
	return ResultNetworkError
}
