# `telegram-relay` — Alertmanager -> Telegram Adapter

## Overview

Builds the new adapter service `phase-6-plan.md`'s Decisions #4 commits
to: Alertmanager's generic webhook receiver POSTs a firing/resolved alert
group to it, and it forwards a human-readable message to the Telegram Bot
API's `sendMessage`. Written as a real Go binary — deliberately not an
off-the-shelf `alertmanager-telegram-bridge` container — for the same
reason every other piece of this project is hand-built: it's the
mechanism being practiced, not the alert itself.

## Requirements

### A fourth binary, following this project's established `cmd/*` shape

`coding-standards.md`'s "Backend Architecture" section already states the
convention every binary in `backend/cmd/` follows: a `main.go` that only
loads config and calls `Run(cfg)`, and a `run.go` (same package `main`)
holding the actual composition root. `cmd/telegram-relay/{main.go,run.go}`
follows it exactly, alongside `cmd/server`, `cmd/ws-gateway`, `cmd/
consumer`. `backend/Dockerfile` gains a fourth `go build -o /out/telegram-
relay ./cmd/telegram-relay` line, matching the existing three.

### `internal/telegramrelay` package

Not shaped like a REST domain (`Handler`/`Service`/`Repository` from
`coding-standards.md`) — there's no database, no business entity, just
"decode Alertmanager's webhook payload, format a message, call one HTTP
API." A small `Relay` type is proportionate:

```go
// internal/telegramrelay/relay.go
type Relay struct {
    botToken string
    chatID   string
    client   *http.Client
}

func NewRelay(botToken, chatID string) *Relay

// Notify formats alerts into a Telegram message and calls the Bot API's
// sendMessage. Alerts is Alertmanager's own webhook payload shape
// (github.com/prometheus/alertmanager/template.Data, or a local subset
// of it — no need for the full upstream type when only Status,
// GroupLabels, and each Alert's Labels/Annotations/StartsAt are used).
func (r *Relay) Notify(ctx context.Context, alerts AlertmanagerWebhook) error
```

### `POST /alert` handler

`internal/telegramrelay/handler.go`, `NewAlertHandler(relay *Relay)
http.HandlerFunc` — decodes Alertmanager's webhook JSON body (see
`alerting/alert-rules.md`'s `webhook_configs`), calls `Relay.Notify`,
responds `200` regardless of whether the Telegram call itself succeeded
(Alertmanager retries a webhook on non-2xx — logging a Telegram API
failure and still returning `200` avoids Alertmanager hammering this
service with retries for a problem retrying won't fix, e.g. an invalid
bot token). A repeated Telegram failure is exactly the kind of thing
`aviron_telegram_relay_errors_total` (a small metric this binary should
also expose via its own `/metrics`, same `metrics/metrics-parity.md`
pattern) would surface.

### Message formatting

One Telegram message per Alertmanager webhook call (already grouped by
`alerting/alert-rules.md`'s own `group_by: ["alertname", "app"]`), not
one per individual alert inside the group — avoids a burst of `N`
messages for `N` alerts that fired together for the same underlying
cause:

```text
🔴 FIRING: TickLatencySLOBurn (race-service)
race-service tick latency p99 above 200ms for 10m
```

`✅ RESOLVED: ...` for `status: "resolved"` webhook calls
(`send_resolved: true` on the Alertmanager side, already set in
`alerting/alert-rules.md`).

### Config

New `internal/config` fields (or a small dedicated config struct,
consistent with `internal/wsgateway.Config`'s own precedent for a
binary-specific config type rather than overloading the shared one):
`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TELEGRAM_RELAY_LISTEN_ADDR`.
Both secret values come from a Kubernetes `Secret`
(`aviron-secret` gains two keys, or a dedicated `telegram-secret` —
either is consistent with this project's existing pattern, confirm the
smaller-blast-radius option — a dedicated secret — at `start`), never
committed, matching every other credential this project already handles
this way (`JWT_SECRET`, `POSTGRES_PASSWORD`).

### Kubernetes manifests

`deploy/k8s/telegram-relay/deployment.yaml` + `service.yaml`, same
`Deployment` + `ClusterIP` `Service` shape as `consumer`'s new `Service`
(`metrics/metrics-parity.md`) — no `Ingress` (Alertmanager, an in-cluster
caller, is its only consumer), `replicas: 1` (stateless, low-volume,
no horizontal-scaling story needed, same call `k8s-consumer-deploy.md`
already made for `consumer`).

## Concurrency

- One `http.Server`, one handler, no shared mutable state beyond the
  `*http.Client` (safe for concurrent use by its own documented
  contract) — nothing here approaches the complexity `room-actor-core.md`
  exists to manage; a stateless webhook-to-webhook relay needs no
  single-writer design.

## Data

```go
// internal/telegramrelay/types.go
type AlertmanagerWebhook struct {
    Status      string            `json:"status"` // "firing" | "resolved"
    GroupLabels map[string]string `json:"groupLabels"`
    Alerts      []struct {
        Status      string            `json:"status"`
        Labels      map[string]string `json:"labels"`
        Annotations map[string]string `json:"annotations"`
    } `json:"alerts"`
}
```

## Notes

- Getting a real Telegram bot token/chat ID is a one-time manual step
  outside this codebase (message `@BotFather`, create a bot, message the
  bot once to learn its own chat ID via `getUpdates`) — not something
  this spec or any Go code automates, same category as this project's
  existing manual `kind`/`kubectl`/Helm setup steps.
- Depends on nothing else in this phase to *build* — pure new code, no
  dependency on Prometheus/Tempo/EFK/Grafana. `alerting/alert-rules.md`
  depends on *this* spec's Service existing at the URL its
  `alertmanager.yml` already references.
- `verification/phase-6-verification.md` is where a real alert actually
  reaches a real Telegram chat, proving the whole chain rather than just
  this service's own unit-level behavior.
- This `/alert` endpoint ends up with two callers, not one:
  Alertmanager (`alerting/alert-rules.md`, and later `alerting/trace-
  alert-rules.md`'s rule through the same Alertmanager) and, separately,
  Grafana's own Unified Alerting (`alerting/log-alert-rules.md`) via a
  webhook contact point. Grafana's webhook payload is expected to be
  structurally close enough to Alertmanager's classic format that
  `AlertmanagerWebhook` decodes both without a format-specific branch —
  `log-alert-rules.md` flags confirming that against a real Grafana
  instance as an open item, not assumed here.
