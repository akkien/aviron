package wsgateway

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signTestSessionToken(t *testing.T, secret []byte, raceID, userID string, expiresIn time.Duration) string {
	t.Helper()

	claims := jwt.MapClaims{
		"race_id": raceID,
		"user_id": userID,
		"exp":     time.Now().Add(expiresIn).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign test session token: %v", err)
	}
	return signed
}

func TestVerifySessionToken_OK(t *testing.T) {
	secret := []byte("test-secret")
	token := signTestSessionToken(t, secret, "race-1", "user-1", time.Hour)

	userID, raceID, err := verifySessionToken(token, secret)
	if err != nil {
		t.Fatalf("verifySessionToken() error = %v", err)
	}
	if userID != "user-1" {
		t.Errorf("userID = %q, want %q", userID, "user-1")
	}
	if raceID != "race-1" {
		t.Errorf("raceID = %q, want %q", raceID, "race-1")
	}
}

func TestVerifySessionToken_Expired(t *testing.T) {
	secret := []byte("test-secret")
	token := signTestSessionToken(t, secret, "race-1", "user-1", -time.Hour)

	_, _, err := verifySessionToken(token, secret)
	if err == nil {
		t.Fatal("verifySessionToken() error = nil, want an error for an expired token")
	}
}

func TestVerifySessionToken_WrongSecret(t *testing.T) {
	token := signTestSessionToken(t, []byte("right-secret"), "race-1", "user-1", time.Hour)

	_, _, err := verifySessionToken(token, []byte("wrong-secret"))
	if err == nil {
		t.Fatal("verifySessionToken() error = nil, want an error for a token signed with a different secret")
	}
}

func TestVerifySessionToken_Malformed(t *testing.T) {
	_, _, err := verifySessionToken("not-a-jwt", []byte("test-secret"))
	if err == nil {
		t.Fatal("verifySessionToken() error = nil, want an error for a malformed token")
	}
}

func TestVerifySessionToken_MissingClaims(t *testing.T) {
	secret := []byte("test-secret")
	claims := jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	_, _, err = verifySessionToken(signed, secret)
	if err == nil {
		t.Fatal("verifySessionToken() error = nil, want an error for missing race_id/user_id claims")
	}
}
