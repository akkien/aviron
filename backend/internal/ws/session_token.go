package ws

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// verifySessionToken checks the per-race session_token internal/race's
// JoinRace issues (race_id/user_id claims, 6h TTL — internal/race/service.go).
// It deliberately reimplements the same parse/verify shape middleware.Auth
// uses for the main JWT rather than importing anything from internal/race
// or internal/middleware: this token's claims (race_id/user_id) differ from
// the main JWT's (sub/email), and this package isn't meant to share code
// with internal/race beyond relying on the same signing convention.
func verifySessionToken(tokenString string, jwtSecret []byte) (userID, raceID string, err error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", "", fmt.Errorf("ws: invalid session token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("ws: invalid session token claims")
	}

	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil || expiresAt.Before(time.Now()) {
		return "", "", fmt.Errorf("ws: session token expired")
	}

	userID, ok = claims["user_id"].(string)
	if !ok || userID == "" {
		return "", "", fmt.Errorf("ws: session token missing user_id claim")
	}

	raceID, ok = claims["race_id"].(string)
	if !ok || raceID == "" {
		return "", "", fmt.Errorf("ws: session token missing race_id claim")
	}

	return userID, raceID, nil
}
