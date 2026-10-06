package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"workflow-optimizer/internal/api/httpx"
)

// statusRecorder remembers the response status for logging and recovery.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Recover turns a handler panic into a 500 INTERNAL_ERROR. The panic value
// and stack are logged server-side only.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger.ErrorContext(r.Context(), "handler panic", "request_id", httpx.RequestID(r.Context()),
					"method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if rec.status == 0 {
					httpx.WriteError(rec, r, nil, errors.New("panic"))
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// Log writes one access-log line per request: method, path, status,
// duration, request ID. Headers, query strings and bodies are never logged.
func Log(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http request", "request_id", httpx.RequestID(r.Context()),
				"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}
