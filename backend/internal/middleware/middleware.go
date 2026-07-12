package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/thirzq/dockerledger/internal/telemetry"
)

// RequestID reads X-Request-ID from the incoming request or generates a new
// short ID, stores it in the request context, and sets it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = shortID()
		}
		ctx := context.WithValue(r.Context(), telemetry.RequestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func shortID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// AccessLog wraps an http.Handler and emits a structured JSON log line for
// every request containing method, path, status, latency, and request_id.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsed := time.Since(start)

		telemetry.WithRequestID(r.Context()).
			Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"latency_ms", elapsed.Milliseconds(),
			)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}