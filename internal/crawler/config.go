package crawler

import (
	"fmt"
	"net/url"
	"time"
)

type Config struct {
	StartURL       string
	RequestTimeout time.Duration
	MaxDepth       int
	MaxPages       int
}

func (c Config) Validate() error {
	_, err := validateConfig(c)
	return err
}

func validateConfig(config Config) (*url.URL, error) {
	if config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}
	if config.MaxDepth < 0 {
		return nil, fmt.Errorf("maximum depth must be zero or greater")
	}
	if config.MaxPages <= 0 {
		return nil, fmt.Errorf("maximum pages must be positive")
	}

	startURL, err := normalizeStartURL(config.StartURL)
	if err != nil {
		return nil, fmt.Errorf("start URL: %w", err)
	}
	return startURL, nil
}
