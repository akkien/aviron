package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
)

func signToken(t *testing.T, secret []byte, exp time.Time) string {
	t.Helper()

	claims := jwt.MapClaims{
		"sub":   "user-1",
		"email": "alice@example.com",
		"exp":   exp.Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func TestAuth_ValidToken(t *testing.T) {
	secret := []byte("test-secret")
	token := signToken(t, secret, time.Now().Add(time.Hour))

	var gotUserID string
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotUserID, _ = middleware.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("wrapped handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotUserID != "user-1" {
		t.Errorf("UserIDFromContext = %q, want %q", gotUserID, "user-1")
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	middleware.Auth([]byte("test-secret"))(next).ServeHTTP(rec, req)

	if called {
		t.Error("wrapped handler was called, want it skipped")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuth_MalformedHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"no bearer prefix", "sometoken"},
		{"empty token after prefix", "Bearer "},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", tt.header)
			rec := httptest.NewRecorder()

			middleware.Auth([]byte("test-secret"))(next).ServeHTTP(rec, req)

			if called {
				t.Error("wrapped handler was called, want it skipped")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuth_InvalidSignature(t *testing.T) {
	token := signToken(t, []byte("wrong-secret"), time.Now().Add(time.Hour))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth([]byte("test-secret"))(next).ServeHTTP(rec, req)

	if called {
		t.Error("wrapped handler was called, want it skipped")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuth_ExpiredToken(t *testing.T) {
	secret := []byte("test-secret")
	token := signToken(t, secret, time.Now().Add(-time.Hour))

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	middleware.Auth(secret)(next).ServeHTTP(rec, req)

	if called {
		t.Error("wrapped handler was called, want it skipped")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
