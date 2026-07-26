# k6 Load Test

Simulates the full real client flow — register, log in, create/join/start
a race, open the real `GET /ws` handshake, stream paced `telemetry`
messages until the race finishes — against a real running backend
instance. See `context/features/phase3/load-testing/k6-load-test.md` for
the full spec and design rationale.

This tool only generates load and gives you a way to watch what happens
while it runs. It does **not** fix whatever it finds — that's separate,
follow-up work once a real run produces a concrete finding to point at.

(Testing against ≥2 backend instances plus `race-router`? See
`load/multi-instance-check.md` instead — a different tool, for correctness
verification rather than load.)

## Prerequisites

- The [k6](https://k6.io/) binary installed (`brew install k6` on macOS).
- Local Postgres running: `docker compose up -d postgres` from the repo
  root.
- The backend running against it: `make start` (or `make run`) from
  `backend/`.

## Running it

From `backend/`:

```sh
make loadtest
```

Or directly:

```sh
k6 run load/scenarios/race-lifecycle.js
```

## Scale knobs

All tunable via `-e`, without editing the script:

| Variable | Default | Meaning |
| --- | --- | --- |
| `BASE_URL` | `http://localhost:8080` | Backend base URL (HTTP; the WS URL is derived from it) |
| `NUM_RACES` | `5` | Number of concurrent races |
| `VUS_PER_RACE` | `8` | Participants per race (must be `<= 10`, `race.MaxParticipants`) |
| `DISTANCE_METERS` | `30` | Target word count per race — how long each race lasts |

Total simulated connections = `NUM_RACES * VUS_PER_RACE`. Start small and
ramp up across repeated runs rather than picking one number blind:

```sh
k6 run -e NUM_RACES=10 -e VUS_PER_RACE=10 load/scenarios/race-lifecycle.js
```

## Watching it happen

k6 prints its own built-in HTTP/WS metrics (request duration, error rate,
connection duration) when the run finishes — useful for *symptoms*
(rising latency, dropped connections), but not *why*.

For the "why" half, cross-reference against `GET /metrics`
(`observability/prometheus-metrics.md`) while the run is in progress. This
project has no Prometheus server/Grafana deployed — nothing scrapes or
stores `/metrics` over time yet — so the simplest way to watch it live is
a manual snapshot loop in a second terminal, started just before `make
loadtest`:

```sh
watch -n2 'curl -s localhost:8080/metrics | grep ^aviron_'
```

Or to keep a timestamped log to diff before/during/after a run:

```sh
while true; do
  { date -u +%FT%TZ; curl -s localhost:8080/metrics | grep ^aviron_; echo; } >> metrics-snapshots.log
  sleep 2
done
```

The one signal worth watching most closely: `aviron_rooms_active` and
goroutine count (`go_goroutines`) that keep climbing *after* every VU has
disconnected, not just during the run — that's the concrete signature of
a leak, not just load. For anything `/metrics` shows going wrong, `go
tool pprof` (`observability/pprof.md`) is the next step to find exactly
which code path is responsible:

```sh
go tool pprof -http=:8081 http://localhost:8080/debug/pprof/goroutine
```

## What this first pass does not include

- Deliberate disconnect/reconnect chaos (a subset of VUs abruptly closing
  mid-race) — a natural follow-up once steady-state numbers are
  understood, not bundled into this first version.
- Testing actual horizontal scaling (Redis, ≥2 backend instances) — Phase
  4 territory. This is scoped to "how far does one instance go."
