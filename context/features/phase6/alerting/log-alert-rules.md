# Log-Based Alert Rule — Grafana Alerting on Elasticsearch

## Overview

`alerting/alert-rules.md`'s 8 rules all evaluate PromQL against
Prometheus — they can tell you a metric crossed a threshold, but nothing
in that pipeline can look at log *content*. Alertmanager itself only
speaks Prometheus; it has no query engine for Elasticsearch. Grafana's
own Unified Alerting, already running as part of `dashboards/grafana-
deploy.md`'s Grafana deployment, can alert on any provisioned
datasource — including the Elasticsearch datasource that spec already
wires up (`aviron-logs` index) — so this one log-based rule lives there
instead of inventing a second log-querying alerting engine.

No new Deployment/Service: Unified Alerting is a built-in Grafana
feature (enabled by default, Grafana >= 8), so this spec only adds
provisioning config to the Grafana pod `dashboards/grafana-deploy.md`
already deploys.

## Requirements

### One rule: `LogErrorRateHigh`

Simple and easy to trigger deliberately for testing: count of
`level:ERROR` documents in the `aviron-logs` index over the last 5
minutes, across any of the three binaries (no `app`/`service` split —
keeping this one rule maximally simple, unlike the per-service
granularity `alerting/alert-rules.md`'s metric rules already have).

```yaml
# deploy/k8s/grafana/configmap-alerting.yaml (rules.yaml key)
apiVersion: 1
groups:
  - orgId: 1
    name: aviron-log-alerts
    folder: aviron
    interval: 1m
    rules:
      - uid: log-error-rate-high
        title: LogErrorRateHigh
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "aviron-logs: more than 10 ERROR-level log lines in the last 5m"
        condition: C
        data:
          - refId: A
            datasourceUid: elasticsearch
            relativeTimeRange: { from: 300, to: 0 }
            model:
              query: "level:ERROR"
              timeField: "@timestamp"
              metrics: [{ type: count, id: "1" }]
              bucketAggs: [{ type: date_histogram, id: "2", field: "@timestamp" }]
          - refId: C
            datasourceUid: __expr__
            model:
              type: threshold
              expression: A
              conditions:
                - evaluator: { type: gt, params: [10] }
```

Exact Elasticsearch query-model JSON (the `model` block under
`refId: A`) depends on the Grafana/Elasticsearch datasource plugin
version actually deployed — confirm the working shape against a real
Grafana instance at `start`; this is a sketch of the intent (count of
matching docs in a 5m window), not a copy-pasteable guarantee.

### Contact point + notification policy — same destination as Alertmanager

Reuses `alerting/telegram-relay.md`'s existing `/alert` endpoint — no
new adapter, no code change expected there (see "Notes" below on
payload compatibility):

```yaml
# deploy/k8s/grafana/configmap-alerting.yaml (contactpoints.yaml key)
apiVersion: 1
contactPoints:
  - orgId: 1
    name: telegram-relay
    receivers:
      - uid: telegram-relay-webhook
        type: webhook
        settings:
          url: http://telegram-relay.aviron.svc.cluster.local:8080/alert
          httpMethod: POST
```

```yaml
# deploy/k8s/grafana/configmap-alerting.yaml (policies.yaml key)
apiVersion: 1
policies:
  - orgId: 1
    receiver: telegram-relay
```

### `grafana/deployment.yaml` gains a fourth provisioning mount

`dashboards/grafana-deploy.md`'s Grafana `Deployment` already mounts
`datasources`, `dashboards-config`, and `dashboards` `ConfigMap`s at
their respective `/etc/grafana/provisioning/*` paths — this spec adds
one more:

```yaml
# addition to the existing volumeMounts/volumes in grafana/deployment.yaml
volumeMounts:
  - name: alerting
    mountPath: /etc/grafana/provisioning/alerting
volumes:
  - name: alerting
    configMap: { name: grafana-alerting }
```

## Verification

- Grafana's own Alerting UI (`kubectl port-forward` into Grafana, per
  `dashboards/grafana-deploy.md`'s existing access pattern) shows
  `LogErrorRateHigh` in "Normal" state once provisioned, with the
  Elasticsearch query evaluating without error.
- **Trigger it on demand**: scale `race-service` to 0 replicas
  (`kubectl scale statefulset race-service --replicas=0 -n aviron`) and
  send a burst of room-less REST requests (e.g.
  `for i in $(seq 1 15); do curl -s -X POST http://<ws-gateway>/races -d '...'; done`)
  — `ws-gateway`'s `Gateway.ServeHTTP` (`internal/wsgateway/gateway.go`)
  logs `"wsgateway: no backends available"` at `ERROR` once per failed
  request, crossing the `> 10` threshold within the 5m window almost
  immediately. Scale `race-service` back to its normal replica count
  afterward.
- Confirm the rule transitions to "Alerting" in Grafana's UI,
  `telegram-relay` receives the webhook POST (check its logs/
  `aviron_telegram_relay_errors_total` stays flat), and a real message
  lands in the configured Telegram chat.

## Notes

- Depends on `dashboards/grafana-deploy.md` (Grafana + the Elasticsearch
  datasource already provisioned) and `alerting/telegram-relay.md` (the
  webhook target already reachable at the URL above) — both must
  already be live to verify end to end, though Kubernetes DNS resolves
  lazily the same way `alerting/alert-rules.md` already notes for its
  own Alertmanager config, so build order isn't a hard blocker for
  writing the manifests.
- **Payload compatibility, confirm at `start`**: Grafana's Unified
  Alerting webhook notifier sends a JSON body structurally close to
  Prometheus Alertmanager's own classic webhook format (`status`,
  `alerts[]` with `labels`/`annotations`, `groupLabels`) since Grafana's
  alerting engine is itself built on a fork of Alertmanager —
  `internal/telegramrelay.AlertmanagerWebhook` (`telegram-relay.md`)
  should decode it without changes, but this needs a real side-by-side
  check against the deployed Grafana version rather than assumed from
  documentation alone.
- Doesn't touch `alerting/alert-rules.md` or Alertmanager at all — this
  is a second, parallel alerting path that happens to terminate at the
  same `telegram-relay` service, not a replacement for the existing
  metrics-based rules.
- Only one rule, deliberately — proving the mechanism (Grafana Alerting
  -> webhook -> Telegram) matters more here than rule coverage; more
  log-based rules can follow the same shape later if needed.
</content>
