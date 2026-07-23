package middleware

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// requestLogAttrsKeyType/requestLogAttrsKey let Auth (an inner middleware,
// applied per-route deep inside httpserver.RegisterRoutes) enrich the
// per-request log line RequestLog emits, even though Auth runs after
// RequestLog in the chain: context.WithValue only takes effect for
// handlers further down the chain, never back up to RequestLog's own stack
// frame once next.ServeHTTP returns. A *pointer* stored in the context,
// though, is a shared mutable object — Auth mutating the struct it points
// to is visible to RequestLog afterward.
type requestLogAttrsKeyType struct{}

var requestLogAttrsKey requestLogAttrsKeyType

type requestLogAttrs struct {
	userID string
}

// RequestLog returns middleware that logs one line per request: method,
// path, status, duration, request_id (from RequestID, if it ran), and
// user_id (if an inner Auth middleware ran and set one via
// setUserIDForLog).
func RequestLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			attrs := &requestLogAttrs{}
			ctx := context.WithValue(r.Context(), requestLogAttrsKey, attrs)

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))

			fields := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", time.Since(start)),
			}
			if requestID, ok := RequestIDFromContext(r.Context()); ok {
				fields = append(fields, slog.String("request_id", requestID))
			}
			if attrs.userID != "" {
				fields = append(fields, slog.String("user_id", attrs.userID))
			}
			logger.Info("http_request", fields...)
		})
	}
}

// setUserIDForLog records userID onto ctx's log-attrs recorder, if
// RequestLog created one, so the per-request summary line can include it.
// Called by Auth once it verifies a request's JWT.
func setUserIDForLog(ctx context.Context, userID string) {
	if attrs, ok := ctx.Value(requestLogAttrsKey).(*requestLogAttrs); ok {
		attrs.userID = userID
	}
}

// statusWriter captures the status code a handler writes, since
// http.ResponseWriter doesn't expose it after the fact.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}

// Hijack forwards to the underlying ResponseWriter's http.Hijacker.
// Without this, statusWriter's embedded field is the http.ResponseWriter
// *interface* (only Header/Write/WriteHeader), not the concrete value's
// full method set — embedding alone does not promote Hijack, so anything
// wrapped in a bare statusWriter would silently stop being hijackable.
// GET /ws (internal/ws/endpoint.go, via coder/websocket.Accept) requires
// exactly this to take over the raw connection for the WebSocket upgrade;
// RequestLog wraps every request, /ws included, so without this forward
// every real WebSocket handshake fails with 501 Not Implemented — found
// via a real k6 load-test run, not a test suite (go test never exercises
// a real hijack, only the fake wsConn in internal/ws's unit tests).
func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := sw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}
