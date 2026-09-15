package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"example.com/graph-test-site-go/internal/crawlercli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := crawlercli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}
