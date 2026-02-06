package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type wrappedWriter struct {
	http.ResponseWriter
	StatusCode int
}

func (w *wrappedWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
	w.StatusCode = statusCode
}

type Middleware func(http.Handler) http.Handler

func CreateMiddlewareStack(xs ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(xs) - 1; i >= 0; i-- {
			x := xs[i]
			next = x(next)
		}

		return next
	}
}

func LoggingMiddleware(next http.Handler) http.Handler {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		wrapped := &wrappedWriter{
			ResponseWriter: w,
			StatusCode:     http.StatusOK,
		}

		next.ServeHTTP(wrapped, r)
		duration := time.Since(start)
		logger.Info("Request completed",
			slog.String("method", r.Method),
			slog.Int("status_code", wrapped.StatusCode),
			slog.String("path", r.URL.String()),
			slog.String("user_agent", r.UserAgent()),
			slog.String("duration", fmt.Sprintf("%dms", duration.Milliseconds())),
		)
	})
}
