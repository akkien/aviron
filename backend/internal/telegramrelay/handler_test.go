package telegramrelay

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeErrorRecorder struct{ count int }

func (f *fakeErrorRecorder) IncError() { f.count++ }

func newTestRelay(t *testing.T, telegramStatus int) *Relay {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(telegramStatus)
	}))
	t.Cleanup(srv.Close)

	relay := NewRelay("token", "chat-id")
	relay.baseURL = srv.URL
	return relay
}

func TestNewAlertHandler_TelegramSucceeds_Returns200NoError(t *testing.T) {
	relay := newTestRelay(t, http.StatusOK)
	errs := &fakeErrorRecorder{}
	handler := NewAlertHandler(relay, errs, slog.New(slog.DiscardHandler))

	body := `{"status":"firing","groupLabels":{"alertname":"X","app":"race-service"},"alerts":[{"status":"firing","annotations":{"summary":"boom"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if errs.count != 0 {
		t.Errorf("errors recorded = %d, want 0 when Telegram call succeeds", errs.count)
	}
}

func TestNewAlertHandler_TelegramFails_StillReturns200ButRecordsError(t *testing.T) {
	relay := newTestRelay(t, http.StatusUnauthorized)
	errs := &fakeErrorRecorder{}
	handler := NewAlertHandler(relay, errs, slog.New(slog.DiscardHandler))

	body := `{"status":"firing","groupLabels":{"alertname":"X"},"alerts":[]}`
	req := httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler(rec, req)

	// Alertmanager/Grafana retry a webhook on non-2xx, and retrying won't
	// fix a bad bot token — so this must stay 200 even though the
	// downstream Telegram call failed (telegram-relay.md).
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 even when the Telegram call fails", rec.Code)
	}
	if errs.count != 1 {
		t.Errorf("errors recorded = %d, want 1 when the Telegram call fails", errs.count)
	}
}

func TestNewAlertHandler_MalformedBody_Returns400(t *testing.T) {
	relay := newTestRelay(t, http.StatusOK)
	errs := &fakeErrorRecorder{}
	handler := NewAlertHandler(relay, errs, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/alert", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a malformed body", rec.Code)
	}
	if errs.count != 0 {
		t.Errorf("errors recorded = %d, want 0 — a decode failure never reaches Notify", errs.count)
	}
}
