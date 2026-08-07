package telegramrelay

// AlertmanagerWebhook is a local subset of Alertmanager's own webhook
// payload shape (github.com/prometheus/alertmanager/template.Data) — only
// Status, GroupLabels, and each Alert's Status/Labels/Annotations are used,
// so depending on the full upstream type isn't worth it. Grafana's own
// Unified Alerting webhook notifier is expected to send a structurally
// close enough payload to decode here too (alerting/log-alert-rules.md).
type AlertmanagerWebhook struct {
	Status      string            `json:"status"`
	GroupLabels map[string]string `json:"groupLabels"`
	Alerts      []Alert           `json:"alerts"`
}

// Alert is one entry in AlertmanagerWebhook.Alerts.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}
