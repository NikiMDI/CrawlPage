package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"example.com/graph-test-site-go/internal/crawler"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("crawler", flag.ContinueOnError)
	flags.SetOutput(stderr)

	startURL := flags.String("url", "", "absolute start URL")
	timeout := flags.Duration("timeout", 2*time.Second, "timeout for the HTTP request")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	inspector, err := crawler.New(crawler.Config{
		StartURL:       *startURL,
		RequestTimeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}

	result, err := inspector.InspectStart(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "inspection failed: %v\n", err)
		return 1
	}

	writeReport(stdout, result)
	return 0
}

func writeReport(output io.Writer, result crawler.PageResult) {
	unique := make(map[string]struct{})
	internalCount := 0
	externalCount := 0
	for _, discovered := range result.Links {
		unique[discovered.URL] = struct{}{}
		switch discovered.Kind {
		case crawler.LinkInternal:
			internalCount++
		case crawler.LinkExternal:
			externalCount++
		}
	}

	fmt.Fprintln(output, "Stage 1: start page inspection")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Level 1 — summary")
	fmt.Fprintf(output, "Start URL:          %s\n", result.URL)
	fmt.Fprintf(output, "HTTP status:        %s\n", result.Status)
	fmt.Fprintln(output, "Pages checked:      1")
	fmt.Fprintf(output, "Links discovered:   %d\n", result.LinksDiscovered)
	fmt.Fprintf(output, "Unique HTTP links:  %d\n", len(unique))
	fmt.Fprintf(output, "Internal links:     %d\n", internalCount)
	fmt.Fprintf(output, "External links:     %d\n", externalCount)
	fmt.Fprintf(output, "Skipped links:      %d\n", len(result.Skipped))

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Level 2 — discovered links")
	if len(result.Links) == 0 && len(result.Skipped) == 0 {
		fmt.Fprintln(output, "No links were extracted from this response.")
		return
	}

	for _, discovered := range result.Links {
		fmt.Fprintln(output)
		fmt.Fprintln(output, discovered.Kind)
		fmt.Fprintf(output, "Source: %s\n", discovered.SourceURL)
		fmt.Fprintf(output, "Raw:    %s\n", discovered.RawHref)
		fmt.Fprintf(output, "URL:    %s\n", discovered.URL)
	}
	for _, skipped := range result.Skipped {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "SKIPPED")
		fmt.Fprintf(output, "Source: %s\n", skipped.SourceURL)
		fmt.Fprintf(output, "Raw:    %s\n", skipped.RawHref)
		fmt.Fprintf(output, "Reason: %s\n", strings.TrimSpace(skipped.Reason))
	}
}
