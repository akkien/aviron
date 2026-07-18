package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akkien/aviron/internal/auth"
)

func newTestHandler() *auth.Handler {
	repo := newFakeRepository()
	svc := auth.NewService(repo)
	return auth.NewHandler(svc)
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
	svc := auth.NewService(repo)
	h := auth.NewHandler(svc)

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
