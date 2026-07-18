package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akkien/aviron/internal/auth"
)

func newTestHandler() *auth.AuthHandler {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))
	return auth.NewAuthHandler(svc)
}

func TestHandler_Register_Created(t *testing.T) {
	h := newTestHandler()

	body := `{"email":"alice@example.com","password":"supersecret","display_name":"Alice"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	h.Register(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["email"] != "alice@example.com" {
		t.Errorf("email = %v, want %q", resp["email"], "alice@example.com")
	}
	if _, ok := resp["password"]; ok {
		t.Errorf("response leaked password field: %v", resp)
	}
	if _, ok := resp["password_hash"]; ok {
		t.Errorf("response leaked password_hash field: %v", resp)
	}
}

func TestHandler_Register_ValidationError(t *testing.T) {
	h := newTestHandler()

	body := `{"email":"not-an-email","password":"short","display_name":""}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	h.Register(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandler_Register_DuplicateEmail(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))
	h := auth.NewAuthHandler(svc)

	body := `{"email":"alice@example.com","password":"supersecret","display_name":"Alice"}`

	req1 := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	h.Register(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	rec2 := httptest.NewRecorder()
	h.Register(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec2.Code, http.StatusConflict, rec2.Body.String())
	}
}

func TestHandler_Register_InvalidBody(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()

	h.Register(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandler_Login_OK(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))
	h := auth.NewAuthHandler(svc)

	registerReq := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(
		`{"email":"alice@example.com","password":"supersecret","display_name":"Alice"}`))
	h.Register(httptest.NewRecorder(), registerReq)

	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(
		`{"email":"alice@example.com","password":"supersecret"}`))
	rec := httptest.NewRecorder()
	h.Login(rec, loginReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["token"] == "" || resp["token"] == nil {
		t.Errorf("token = %v, want a non-empty JWT", resp["token"])
	}
	if resp["expires_at"] == "" || resp["expires_at"] == nil {
		t.Errorf("expires_at = %v, want a non-empty timestamp", resp["expires_at"])
	}
}

func TestHandler_Login_Unauthorized(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"wrong password", `{"email":"alice@example.com","password":"wrongpassword"}`},
		{"unknown email", `{"email":"nobody@example.com","password":"supersecret"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepository()
			svc := auth.NewAuthService(repo, []byte("test-secret"))
			h := auth.NewAuthHandler(svc)

			registerReq := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(
				`{"email":"alice@example.com","password":"supersecret","display_name":"Alice"}`))
			h.Register(httptest.NewRecorder(), registerReq)

			loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			h.Login(rec, loginReq)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestHandler_Login_InvalidBody(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()

	h.Login(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
