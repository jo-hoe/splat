package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// ctxKey is the unexported type used for context keys in this package.
type ctxKey int

const (
	// ctxRequestID is the context key holding the per-request ID.
	ctxRequestID ctxKey = iota
)

// requestID is middleware that assigns a random 16-hex request ID to each
// request, stores it in context, and surfaces it via the X-Request-ID
// response header. The ID can be retrieved via RequestIDFromContext.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns a 16-hex random string. On the (extremely
// unlikely) event of crypto/rand failure it falls back to a static value
// so the middleware never panics.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// RequestIDFromContext returns the request ID stored in ctx by the
// requestID middleware, or "" if none is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the status before delegating.
func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wrote {
		sr.status = code
		sr.wrote = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

// Write ensures status defaults to 200 on the first body write.
func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wrote {
		sr.status = http.StatusOK
		sr.wrote = true
	}
	return sr.ResponseWriter.Write(b)
}

// accessLog returns middleware that logs each request as a single
// structured slog entry with method, path, status, and duration_ms.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sr, r)
			logger.Info("request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sr.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// recoverPanic returns middleware that recovers from panics in downstream
// handlers, logs them, and replies with a 500.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic",
						"request_id", RequestIDFromContext(r.Context()),
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// chain composes middleware in the order given. The first middleware
// wraps the outermost layer.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
