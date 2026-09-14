package crawler

import (
	"fmt"
	"net/url"
	"time"
)

type Config struct {
	StartURL       string
	RequestTimeout time.Duration
}

func (c Config) Validate() error {
	_, err := validateConfig(c)
	return err
}

func validateConfig(config Config) (*url.URL, error) {
	if config.RequestTimeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}

	startURL, err := normalizeStartURL(config.StartURL)
	if err != nil {
		return nil, fmt.Errorf("start URL: %w", err)
	}
	return startURL, nil
}
