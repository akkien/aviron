package telegramrelay

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorRecorder lets NewAlertHandler record a failed Notify call without
// importing prometheus directly — internal/metrics.TelegramRelayMetrics
// satisfies this, the same decoupling internal/consumer's MetricsRecorder
// interface already uses for the same reason.
type ErrorRecorder interface {
	IncError()
}

// NewAlertHandler decodes the webhook body, calls relay.Notify, and always
// responds 200 regardless of whether the Telegram call itself succeeded —
// the caller (Alertmanager, or Grafana's Unified Alerting) retries a
// webhook on non-2xx, and retrying won't fix a bad bot token, so a
// Notify failure is logged and counted (errors) instead of surfaced as a
// non-2xx response.
func NewAlertHandler(relay *Relay, errors ErrorRecorder, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload AlertmanagerWebhook
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			logger.Error("telegramrelay: decode webhook body failed", slog.Any("error", err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err := relay.Notify(r.Context(), payload); err != nil {
			logger.Error("telegramrelay: notify failed", slog.Any("error", err))
			errors.IncError()
		}

		w.WriteHeader(http.StatusOK)
	}
}
