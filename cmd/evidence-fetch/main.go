// evidence-fetch runs the unprotected reference handler on loopback only.
// A public listener must be introduced together with the x402 payment gate.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gnanam1990/flowops/internal/evidencefetch"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "loopback listener address")
	maxResponseBytes := flag.Int64("max-response-bytes", 2<<20, "maximum fetched response bytes")
	maxOutputBytes := flag.Int64("max-output-bytes", 256<<10, "maximum returned normalized-text bytes")
	fetchTimeout := flag.Duration("fetch-timeout", 15*time.Second, "upstream fetch timeout")
	flag.Parse()

	if err := validateLoopbackListener(*listen); err != nil {
		log.Fatal(err)
	}
	service, err := evidencefetch.New(evidencefetch.Config{
		MaxResponseBytes: *maxResponseBytes,
		MaxOutputBytes:   *maxOutputBytes,
		Timeout:          *fetchTimeout,
	})
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           evidencefetch.Handler(service),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      *fetchTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("FlowOps Evidence Fetch listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func validateLoopbackListener(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address must include an explicit loopback host and port: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	addressIP := net.ParseIP(host)
	if addressIP == nil || !addressIP.IsLoopback() {
		return errors.New("unprotected evidence-fetch server may listen only on loopback")
	}
	return nil
}
