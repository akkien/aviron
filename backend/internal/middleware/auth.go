package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/akkien/aviron/internal/httpx"
)

type contextKey int

const userIDContextKey contextKey = iota

// Auth wraps a handler so it only runs for requests carrying a valid,
// unexpired JWT signed with jwtSecret. On success, the authenticated user id
// is attached to the request context (read it back with UserIDFromContext).
func Auth(jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const prefix = "Bearer "
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, prefix) {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			tokenString := strings.TrimPrefix(authHeader, prefix)
			if tokenString == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrTokenSignatureInvalid
				}
				return jwtSecret, nil
			})
			// jwt.Parse's default validator already rejects an expired token
			// (it parses into jwt.MapClaims, which implements
			// GetExpirationTime(), so err would already be non-nil here) —
			// but check explicitly too, so the expiry guarantee is visible
			// here rather than resting on an implicit library default.
			if err != nil || !token.Valid {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			expiresAt, err := claims.GetExpirationTime()
			if err != nil || expiresAt == nil || expiresAt.Before(time.Now()) {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			userID, ok := claims["sub"].(string)
			if !ok || userID == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), userIDContextKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext reads the user id Auth attached to ctx.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDContextKey).(string)
	return id, ok
}
