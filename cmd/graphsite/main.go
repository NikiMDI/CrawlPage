package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/graph-test-site-go/internal/site"
)

func main() {
	address := flag.String("addr", "127.0.0.1:8080", "local address for the HTTP server")
	slowDelay := flag.Duration("slow-delay", 5*time.Second, "delay before /slow.html responds")
	flag.Parse()

	handler, err := site.NewHandler(site.Config{SlowDelay: *slowDelay})
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatalf("listen on %s: %v", *address, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	log.Printf("Graph test site: http://%s/index.html", listener.Addr())
	log.Printf("Slow page delay: %s", *slowDelay)

	shutdownSignal, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	select {
	case serveErr := <-serverErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("serve HTTP: %v", serveErr)
		}
	case <-shutdownSignal.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownContext); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}

		serveErr := <-serverErrors
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("serve HTTP during shutdown: %v", serveErr)
		}
	}
}
