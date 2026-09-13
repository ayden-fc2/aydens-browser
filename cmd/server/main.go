package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ayden-fc2/aydens-browser/internal/search"
	"github.com/ayden-fc2/aydens-browser/internal/webguard"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := &http.Client{Timeout: 2 * time.Second}
		r, e := c.Get("http://127.0.0.1:8080/healthz")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	if e := run(); e != nil {
		slog.Error("web tools stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	path := os.Getenv("WEB_TOOLS_TOKEN_FILE")
	if path == "" {
		path = "/run/secrets/web_tools_token"
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return errors.New("cannot read web tools API key")
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return errors.New("API key must contain at least 32 characters")
	}
	proxy := os.Getenv("SEARCH_PROXY_URL")
	worker, e := search.NewSearchWorker(proxy, os.Getenv("SEARXNG_BASE_URL"), os.Getenv("BROWSER_BASE_URL"), token)
	if e != nil {
		return e
	}
	guard, e := webguard.New(proxy, os.Getenv("BROWSER_DENY_IPS"))
	if e != nil {
		return e
	}
	api := &http.Server{Addr: ":8080", Handler: worker.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second}
	egress := &http.Server{Addr: ":8081", Handler: guard, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 40 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- api.ListenAndServe() }()
	go func() { errs <- egress.ListenAndServe() }()
	select {
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	api.Shutdown(shutdown)
	egress.Shutdown(shutdown)
	return nil
}
