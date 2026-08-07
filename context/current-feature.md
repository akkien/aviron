# Current Feature: telegram-relay

## Status

Not Started

## Goals

- A fourth Go binary, `cmd/telegram-relay`, that receives Alertmanager's
  generic webhook and forwards a human-readable message to Telegram.
- Alertmanager's `alerting/alert-rules.md` webhook config can POST to
  this service's `/alert` and a real Telegram chat receives the message.
- `aviron_telegram_relay_errors_total` metric exposed via `/metrics`,
  matching the `metrics/metrics-parity.md` pattern already used by
  `ws-gateway`/`consumer`.
- Deployed to `deploy/k8s/telegram-relay/` (`Deployment` + `Service`,
  `replicas: 1`, no `Ingress`) and reachable at
  `http://telegram-relay.aviron.svc.cluster.local:8080/alert`, the exact
  URL `alerting/alert-rules.md`'s `alertmanager.yml` already references.

## Explain

- Spec: `context/features/phase6/alerting/telegram-relay.md`, part of
  Phase 6 observability (`context/features/phase6/phase-6-plan.md`
  Decisions #4).
- Not a REST domain (no `Handler`/`Service`/`Repository` split) — no
  database, no business entity involved. Just "decode Alertmanager's
  webhook payload, format a message, call one HTTP API," so a single
  small `Relay` type in `internal/telegramrelay` is proportionate.
- `cmd/telegram-relay/{main.go,run.go}` follows the same composition-root
  split as every other binary (`cmd/server`, `cmd/ws-gateway`,
  `cmd/consumer`): `main.go` loads config and calls `Run(cfg)`; `run.go`
  is the actual composition root.
- `internal/telegramrelay/relay.go` — `Relay{botToken, chatID, client}`,
  `NewRelay(botToken, chatID string) *Relay`,
  `(*Relay).Notify(ctx, AlertmanagerWebhook) error` — formats alerts into
  one Telegram message per webhook call (already grouped by
  `alert-rules.md`'s `group_by: ["alertname", "app"]`) and calls the
  Telegram Bot API's `sendMessage`.
- `internal/telegramrelay/handler.go` —
  `NewAlertHandler(relay *Relay) http.HandlerFunc` serving `POST /alert`:
  decode JSON body, call `Notify`, always respond `200` regardless of
  whether the Telegram call itself succeeded (Alertmanager retries a
  webhook on non-2xx; retrying won't fix a bad bot token, so log the
  failure and increment `aviron_telegram_relay_errors_total` instead).
- `internal/telegramrelay/types.go` — local `AlertmanagerWebhook` struct
  (`Status`, `GroupLabels`, `Alerts[].{Status,Labels,Annotations}`), a
  subset of `template.Data`, not the full upstream type.
- Message format: `🔴 FIRING: <alertname> (<app>)` + annotation summary
  line for `status: "firing"`; `✅ RESOLVED: ...` for `status: "resolved"`.
- Config: new fields for `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`,
  `TELEGRAM_RELAY_LISTEN_ADDR` — a dedicated config struct
  (`internal/telegramrelay.Config` or similar), consistent with
  `internal/wsgateway.Config`'s precedent of a binary-specific config
  type rather than overloading the shared one.
- `backend/Dockerfile` gains a fourth `go build -o /out/telegram-relay
  ./cmd/telegram-relay` line, matching the existing three.
- Concurrency: one `http.Server`, one handler, no shared mutable state
  beyond `*http.Client` (safe for concurrent use per its own contract) —
  no single-writer/room-actor complexity needed here.

## Plan

- Confirm at `start`: dedicated `telegram-secret` (smaller blast radius)
  vs. adding two keys to the existing `aviron-secret` — spec flags this
  as an open decision to confirm before implementing.
- Build order: `internal/telegramrelay` package (types, relay, handler)
  → `cmd/telegram-relay` binary → Dockerfile line → k8s manifests
  (`deploy/k8s/telegram-relay/deployment.yaml` + `service.yaml`, modeled
  on `deploy/k8s/consumer/`'s `Deployment`/`Service` shape: `replicas: 1`,
  `ClusterIP` Service, no readiness/liveness probe since this binary's
  only HTTP surface is `/alert` + `/metrics`, prometheus.io scrape
  annotations for the metrics endpoint).
- Metric: add `aviron_telegram_relay_errors_total` (Counter), incremented
  whenever `Relay.Notify`'s Telegram API call fails.
- Unit tests: `Relay.Notify` message formatting (firing vs. resolved,
  grouped alerts → one message), handler always returns 200 even when
  the mocked Telegram call errors.
- No dependency on Prometheus/Tempo/EFK/Grafana to build — pure new code.
  `alerting/alert-rules.md` depends on this service existing at the URL
  it already references, not the other way around.

## Notes

- Real Telegram bot token/chat ID is a one-time manual step outside this
  codebase (message `@BotFather`, create a bot, message it once to learn
  its chat ID via `getUpdates`) — not automated by this spec or any Go
  code, same category as this project's existing manual `kind`/
  `kubectl`/Helm setup steps.
- Full end-to-end proof (a real alert reaching a real Telegram chat) is
  `verification/phase-6-verification.md`'s job, not this feature's own
  unit-level verification.
- Added since this feature was loaded: `/alert` will eventually get a
  second caller besides Alertmanager — Grafana's own Unified Alerting
  (`alerting/log-alert-rules.md`, not yet started), via a webhook
  contact point pointed at this same URL. Its payload is expected to be
  structurally close enough to Alertmanager's classic format that
  `AlertmanagerWebhook` decodes both without a format-specific branch,
  but that's unverified — no code change needed for *this* feature, just
  worth knowing the decode path isn't Alertmanager-exclusive long term.
