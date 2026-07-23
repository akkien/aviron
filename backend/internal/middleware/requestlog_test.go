package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/middleware"
)

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

func decodeLastLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("no log line was written")
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("decode log line: %v (line: %s)", err, line)
	}
	return m
}

func TestRequestLog_LogsMethodPathStatusDuration(t *testing.T) {
	logger, buf := newTestLogger()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/races", nil)
	rec := httptest.NewRecorder()
	middleware.RequestLog(logger)(next).ServeHTTP(rec, req)

	fields := decodeLastLogLine(t, buf)
	if fields["method"] != http.MethodPost {
		t.Errorf("method = %v, want %q", fields["method"], http.MethodPost)
	}
	if fields["path"] != "/races" {
		t.Errorf("path = %v, want %q", fields["path"], "/races")
	}
	if fields["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v, want %d", fields["status"], http.StatusCreated)
	}
	if _, ok := fields["duration"]; !ok {
		t.Error("duration field missing")
	}
	if _, ok := fields["request_id"]; ok {
		t.Error("request_id present, want absent — RequestID never ran in this chain")
	}
	if _, ok := fields["user_id"]; ok {
		t.Error("user_id present, want absent — Auth never ran in this chain")
	}
}

func TestRequestLog_DefaultsStatusOKWhenHandlerNeverCallsWriteHeader(t *testing.T) {
	logger, buf := newTestLogger()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	middleware.RequestLog(logger)(next).ServeHTTP(httptest.NewRecorder(), req)

	fields := decodeLastLogLine(t, buf)
	if fields["status"] != float64(http.StatusOK) {
		t.Errorf("status = %v, want %d", fields["status"], http.StatusOK)
	}
}

func TestRequestLog_IncludesRequestIDWhenRequestIDRanFirst(t *testing.T) {
	logger, buf := newTestLogger()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.RequestID()(middleware.RequestLog(logger)(next))
	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	fields := decodeLastLogLine(t, buf)
	wantID := rec.Header().Get("X-Request-ID")
	if fields["request_id"] != wantID {
		t.Errorf("request_id = %v, want %q", fields["request_id"], wantID)
	}
}

// TestRequestLog_IncludesUserIDWhenAuthRanInside proves the recorder-pointer
// mechanism actually works: RequestLog wraps Auth, so RequestLog's own
// ServeHTTP frame runs to completion (and reads userID back out) only after
// Auth already returned deeper in the call stack — context.WithValue alone
// can't carry that value back up, since Auth calls next with a *new*
// *http.Request built via r.WithContext, never mutating the one RequestLog
// holds a reference to.
func TestRequestLog_IncludesUserIDWhenAuthRanInside(t *testing.T) {
	logger, buf := newTestLogger()
	secret := []byte("test-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.RequestLog(logger)(middleware.Auth(secret)(next))
	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, secret, time.Now().Add(time.Hour)))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	fields := decodeLastLogLine(t, buf)
	if fields["user_id"] != "user-1" {
		t.Errorf("user_id = %v, want %q", fields["user_id"], "user-1")
	}
}

func TestRequestLog_OmitsUserIDWhenAuthRejects(t *testing.T) {
	logger, buf := newTestLogger()
	secret := []byte("test-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.RequestLog(logger)(middleware.Auth(secret)(next))
	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	fields := decodeLastLogLine(t, buf)
	if fields["status"] != float64(http.StatusUnauthorized) {
		t.Errorf("status = %v, want %d", fields["status"], http.StatusUnauthorized)
	}
	if _, ok := fields["user_id"]; ok {
		t.Error("user_id present, want absent — Auth rejected the request")
	}
}
