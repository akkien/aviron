package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDContextKeyType struct{}

var requestIDContextKey requestIDContextKeyType

// RequestID wraps a handler so every request carries a random id, attached
// to the request context (read it back with RequestIDFromContext) and
// echoed back as the X-Request-ID response header so a client-reported bug
// can be correlated against server logs.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := generateRequestID()
			w.Header().Set("X-Request-ID", id)
			ctx := context.WithValue(r.Context(), requestIDContextKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext reads the request id RequestID attached to ctx.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDContextKey).(string)
	return id, ok
}

// generateRequestID returns a random 32-character hex id, crypto/rand
// -backed like internal/race.GenerateRaceID (never math/rand for anything
// identifier-shaped). Unlike GenerateRaceID, the error isn't threaded back
// to the caller: crypto/rand.Read against the OS CSPRNG doesn't fail in
// practice, and there's no reasonable fallback for a request id anyway.
func generateRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
