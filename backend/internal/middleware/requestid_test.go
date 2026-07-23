package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akkien/aviron/internal/middleware"
)

func TestRequestID_AttachesIDAndHeader(t *testing.T) {
	var gotID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = middleware.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	rec := httptest.NewRecorder()

	middleware.RequestID()(next).ServeHTTP(rec, req)

	if gotID == "" {
		t.Fatal("RequestIDFromContext returned empty id")
	}
	if len(gotID) != 32 {
		t.Errorf("id length = %d, want 32 (16 bytes hex-encoded)", len(gotID))
	}
	if header := rec.Header().Get("X-Request-ID"); header != gotID {
		t.Errorf("X-Request-ID header = %q, want %q", header, gotID)
	}
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	var ids []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := middleware.RequestIDFromContext(r.Context())
		ids = append(ids, id)
	})

	handler := middleware.RequestID()(next)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/races", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/races", nil))

	if ids[0] == ids[1] {
		t.Errorf("two requests got the same request id: %q", ids[0])
	}
}

func TestRequestIDFromContext_NotSet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	if _, ok := middleware.RequestIDFromContext(req.Context()); ok {
		t.Error("RequestIDFromContext returned ok=true on a context RequestID never touched")
	}
}
