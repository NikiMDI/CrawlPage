package crawler

import (
	"fmt"
	"net/url"
	"time"
)

const DefaultMaxHTMLBytes int64 = 2 * 1024 * 1024

const DefaultMaxQueue = 1000

const maxHTMLBytesUpperBound int64 = 1<<62 - 1

type Config struct {
	StartURL       string
	RequestTimeout time.Duration
	MaxDepth       int
	MaxPages       int
	MaxRedirects   int
	Concurrency    int
	MaxQueue       int
	MaxHTMLBytes   int64
}

func (c Config) Validate() error {
	c = c.withDefaults()
	_, err := validateConfig(c)
	return err
}

func (c Config) withDefaults() Config {
	if c.MaxHTMLBytes == 0 {
		c.MaxHTMLBytes = DefaultMaxHTMLBytes
	}
	if c.MaxQueue == 0 {
		c.MaxQueue = DefaultMaxQueue
	}
	return c
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
	if config.MaxRedirects < 0 {
		return nil, fmt.Errorf("maximum redirects must be zero or greater")
	}
	if config.Concurrency <= 0 {
		return nil, fmt.Errorf("concurrency must be positive")
	}
	if config.MaxQueue <= 0 {
		return nil, fmt.Errorf("maximum queue size must be positive")
	}
	if config.MaxHTMLBytes <= 0 {
		return nil, fmt.Errorf("maximum HTML bytes must be positive")
	}
	if config.MaxHTMLBytes > maxHTMLBytesUpperBound {
		return nil, fmt.Errorf("maximum HTML bytes is too large")
	}

	startURL, err := normalizeStartURL(config.StartURL)
	if err != nil {
		return nil, fmt.Errorf("start URL: %w", err)
	}
	return startURL, nil
}
