package telegramrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFormatMessage_Firing(t *testing.T) {
	w := AlertmanagerWebhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "TickLatencySLOBurn", "app": "race-service"},
		Alerts: []Alert{
			{Status: "firing", Annotations: map[string]string{"summary": "race-service tick latency p99 above 200ms for 10m"}},
		},
	}

	got := formatMessage(w)
	want := "🔴 FIRING: TickLatencySLOBurn (race-service)\nrace-service tick latency p99 above 200ms for 10m"
	if got != want {
		t.Errorf("formatMessage() = %q, want %q", got, want)
	}
}

func TestFormatMessage_Resolved(t *testing.T) {
	w := AlertmanagerWebhook{
		Status:      "resolved",
		GroupLabels: map[string]string{"alertname": "KafkaConsumerLagHigh", "app": "consumer"},
		Alerts: []Alert{
			{Status: "resolved", Annotations: map[string]string{"summary": "consumer lag on workout.sample exceeds 2000 messages"}},
		},
	}

	got := formatMessage(w)
	want := "✅ RESOLVED: KafkaConsumerLagHigh (consumer)\nconsumer lag on workout.sample exceeds 2000 messages"
	if got != want {
		t.Errorf("formatMessage() = %q, want %q", got, want)
	}
}

func TestFormatMessage_GroupedAlerts_OneMessagePerCall(t *testing.T) {
	w := AlertmanagerWebhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "PodRestartLooping"},
		Alerts: []Alert{
			{Annotations: map[string]string{"summary": "race-service-0 restarted more than 3 times in 15m"}},
			{Annotations: map[string]string{"summary": "race-service-1 restarted more than 3 times in 15m"}},
		},
	}

	got := formatMessage(w)
	if strings.Count(got, "restarted more than 3 times") != 2 {
		t.Errorf("formatMessage() = %q, want both alerts' summaries in one message", got)
	}
	// No "app" label on this rule (it keys off {{ $labels.pod }}, not app) —
	// the title must not render a stray empty "()" pair.
	if strings.Contains(got, "()") {
		t.Errorf("formatMessage() = %q, want no empty parens when app label is absent", got)
	}
}

func TestFormatMessage_NoSummaryAnnotation_TitleOnly(t *testing.T) {
	w := AlertmanagerWebhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "SomeAlert"},
	}

	got := formatMessage(w)
	if got != "🔴 FIRING: SomeAlert" {
		t.Errorf("formatMessage() = %q, want title-only message", got)
	}
}

func TestRelay_Notify_SendsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	relay := NewRelay("test-token", "12345")
	relay.baseURL = srv.URL

	w := AlertmanagerWebhook{
		Status:      "firing",
		GroupLabels: map[string]string{"alertname": "ElevatedErrorRate", "app": "ws-gateway"},
		Alerts:      []Alert{{Annotations: map[string]string{"summary": "ws-gateway error rate above 5% over 5m"}}},
	}

	if err := relay.Notify(context.Background(), w); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("request path = %q, want /bottest-token/sendMessage", gotPath)
	}
	if gotBody["chat_id"] != "12345" {
		t.Errorf("chat_id = %q, want 12345", gotBody["chat_id"])
	}
	if !strings.Contains(gotBody["text"], "ElevatedErrorRate") {
		t.Errorf("text = %q, want it to contain the alertname", gotBody["text"])
	}
}

func TestRelay_Notify_NonOKResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	relay := NewRelay("bad-token", "12345")
	relay.baseURL = srv.URL

	err := relay.Notify(context.Background(), AlertmanagerWebhook{Status: "firing", GroupLabels: map[string]string{"alertname": "X"}})
	if err == nil {
		t.Fatal("Notify() error = nil, want non-nil for a non-2xx Telegram response")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("Notify() error = %v, want it to include the Telegram response body", err)
	}
}
