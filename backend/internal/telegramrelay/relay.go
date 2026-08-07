package telegramrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// telegramAPIBaseURL is the Telegram Bot API's own base URL — overridable
// on a Relay only for tests (Notify's httptest.Server target), never
// meant to be configured in production.
const telegramAPIBaseURL = "https://api.telegram.org"

// Relay formats an Alertmanager webhook payload into a human-readable
// message and forwards it to the Telegram Bot API's sendMessage.
type Relay struct {
	botToken string
	chatID   string
	client   *http.Client
	baseURL  string
}

// NewRelay constructs a Relay for the given bot token/chat ID
// (telegram-relay.md).
func NewRelay(botToken, chatID string) *Relay {
	return &Relay{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 5 * time.Second},
		baseURL:  telegramAPIBaseURL,
	}
}

// Notify formats w into one Telegram message and calls the Bot API's
// sendMessage. Alertmanager (and, separately, Grafana's Unified Alerting —
// alerting/log-alert-rules.md) already groups alerts by alertname+app
// before this is ever invoked, so one webhook call maps to exactly one
// Telegram message here too.
func (r *Relay) Notify(ctx context.Context, w AlertmanagerWebhook) error {
	body, err := json.Marshal(map[string]string{
		"chat_id": r.chatID,
		"text":    formatMessage(w),
	})
	if err != nil {
		return fmt.Errorf("telegramrelay: marshal sendMessage body: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", r.baseURL, r.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegramrelay: build sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegramrelay: sendMessage request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegramrelay: sendMessage returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// formatMessage builds one message per webhook call, per telegram-relay.md
// — every alert in w.Alerts already shares the same alertname/app (the
// caller's own group_by), so only the header icon (firing vs. resolved)
// and each alert's summary annotation vary.
func formatMessage(w AlertmanagerWebhook) string {
	icon := "🔴 FIRING"
	if w.Status == "resolved" {
		icon = "✅ RESOLVED"
	}

	title := fmt.Sprintf("%s: %s", icon, w.GroupLabels["alertname"])
	if app := w.GroupLabels["app"]; app != "" {
		title += fmt.Sprintf(" (%s)", app)
	}

	var lines []string
	for _, a := range w.Alerts {
		if summary := a.Annotations["summary"]; summary != "" {
			lines = append(lines, summary)
		}
	}
	if len(lines) == 0 {
		return title
	}
	return title + "\n" + strings.Join(lines, "\n")
}
