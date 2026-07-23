package middleware_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
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

// hijackableRecorder adds a fake Hijack to httptest.ResponseRecorder,
// since ResponseRecorder itself doesn't implement http.Hijacker.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// TestRequestLog_ForwardsHijack is a regression test for a real bug found
// via a k6 load test run: statusWriter only embeds the http.ResponseWriter
// *interface* (Header/Write/WriteHeader), so without an explicit Hijack
// method, GET /ws's real WebSocket handshake (coder/websocket.Accept,
// which requires http.Hijacker to take over the raw connection) failed
// with 501 Not Implemented for every request that passed through
// RequestLog — which wraps the entire mux, so every request.
func TestRequestLog_ForwardsHijack(t *testing.T) {
	logger, _ := newTestLogger()
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}

	var hijackErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter passed to the handler does not implement http.Hijacker")
		}
		_, _, hijackErr = hj.Hijack()
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	middleware.RequestLog(logger)(next).ServeHTTP(rec, req)

	if hijackErr != nil {
		t.Fatalf("Hijack() returned an error: %v", hijackErr)
	}
	if !rec.hijacked {
		t.Error("underlying ResponseWriter's Hijack was never called — RequestLog's wrapper isn't forwarding it")
	}
}

func TestRequestLog_HijackErrorsWhenUnderlyingWriterDoesNotSupportIt(t *testing.T) {
	logger, _ := newTestLogger()
	rec := httptest.NewRecorder() // does not implement http.Hijacker

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter passed to the handler does not implement http.Hijacker")
		}
		if _, _, err := hj.Hijack(); err == nil {
			t.Error("Hijack() error = nil, want an error since the underlying ResponseWriter doesn't support it")
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	middleware.RequestLog(logger)(next).ServeHTTP(rec, req)
}
