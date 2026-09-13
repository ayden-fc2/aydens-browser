package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Search) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /v1/search", func(w http.ResponseWriter, r *http.Request) {
		result, e := s.RunWithOptions(r.Context(), r.URL.Query().Get("q"), SearchOptions{Topic: r.URL.Query().Get("topic"), TimeRange: r.URL.Query().Get("time_range")})
		if e != nil {
			respond(w, 400, map[string]string{"error": e.Error()})
			return
		}
		respond(w, 200, result)
	})
	mux.HandleFunc("POST /v1/browser/actions", func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<10))
		if e != nil {
			respond(w, 413, map[string]string{"error": "request too large"})
			return
		}
		req, e := http.NewRequestWithContext(r.Context(), "POST", s.BrowserURL+"/v1/actions", bytes.NewReader(b))
		if e != nil {
			respond(w, 503, map[string]string{"error": "browser unavailable"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+s.Token)
		req.Header.Set("Content-Type", "application/json")
		res, e := s.Client.Do(req)
		if e != nil {
			respond(w, 503, map[string]string{"error": "browser unavailable or timed out"})
			return
		}
		defer res.Body.Close()
		if retry := res.Header.Get("Retry-After"); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(res.StatusCode)
		io.Copy(w, io.LimitReader(res.Body, 2<<20))
	})
	slots := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}
		supplied := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		expected := sha256.Sum256([]byte("Bearer " + s.Token))
		if len(s.Token) < 32 || subtle.ConstantTimeCompare(supplied[:], expected[:]) != 1 {
			respond(w, 401, map[string]string{"error": "invalid API key"})
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "5")
			respond(w, 429, map[string]string{"error": "service busy"})
			return
		}
		if r.URL.Path == "/v1/search" && len(r.URL.RawQuery) > 2400 {
			respond(w, 414, map[string]string{"error": "query too long"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 36*time.Second)
		defer cancel()
		started := time.Now()
		mux.ServeHTTP(w, r.WithContext(ctx))
		if strings.HasSuffix(r.URL.Path, "/search") {
			slog.Info("search request", "duration_ms", time.Since(started).Milliseconds(), "query_chars", len([]rune(r.URL.Query().Get("q"))))
		}
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
