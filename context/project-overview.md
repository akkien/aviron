# Side project: Real-time Multiplayer Fitness Backend (inspired by Aviron)

## 0. Business context

Aviron (<avironactive.com>) sells connected fitness equipment (rowers, bikes, treadmills) and turns cardio workouts into a game: users join a **race** against others in real time, view a **leaderboard**, and compete based on workout metrics (stroke rate, watt, RPM, distance...). From the JD (/docs/jd.md), we can infer their actual architecture involves:

- Devices/apps continuously sending **workout telemetry** (pace, distance, power) during a workout.
- Multiple people in **one race** need to see each other's position in near real time, staying consistent even though data arrives from many different machines and networks.
- The system must tolerate frequent **disconnect/reconnect** events (home devices, home wifi, mobile apps).
- A **leaderboard** that aggregates large volumes of workout data (event pipeline, analytics).
- Services running as **multiple instances** (horizontal scaling), so a race's state can't just live in one machine's RAM.

That's why the JD emphasizes: goroutines/channels/context, data races & goroutine leaks, reconnection handling, PostgreSQL, and "strong plus" items like Redis/Kafka/NATS/K8s/ClickHouse.

This project simulates that "workout telemetry" signal with a **typing race** rather than real fitness hardware — see §13 for the concrete mechanic. The real-time sync, reconnection, and leaderboard architecture below is unchanged either way; only the meaning of the per-tick number changes.

## 1. Goal & scope of the side project

**Goal:** practice exactly the skills the JD lists, not rebuild the whole of Aviron. The focus stays on the backend; the frontend is written in React, but only to the extent needed to test the system (no need to polish UI/UX).

**In scope (backend is the focus):**

- Simple auth (JWT, no need for complex OAuth)
- Create/join a real-time "race room" over WebSocket
- Sync race position across multiple clients in a room, ticking at a fixed interval (e.g. 250ms) — position is driven by words typed correctly in a shared typing race (§13), not real fitness telemetry
- Reconnect handling: a client that drops still rejoins the correct race instead of being treated as "quit" immediately
- Persist race results + workout history in PostgreSQL
- Per-user all-time stats (races joined, races won, avg WPM) and a ranked/windowed leaderboard, both queried from Postgres, backing the dashboard's own stat cards
- Horizontal scaling: run ≥2 instances of each backend service; NATS carries real-time room state between them, Redis serves as a lightweight room-ownership registry (§5)
- Observability: structured logs, Prometheus metrics across every binary, and a full distributed-tracing/log-aggregation/alerting stack (§9, §12 Phase 6)
- Testing: unit tests for logic, `go test -race` for concurrency, load testing with k6/ghz
- Frontend: a small React app (Vite), styled with Tailwind CSS and shadcn/ui components (speeds up building consistent forms/buttons without hand-rolled CSS — still no polish investment, per the scope note above) — login screen, create/join race, and a typing-race view (type a shared prompt to move your car) showing participants' positions updating in real time over WebSocket. Real typing input doubles as the "device telemetry," so no simulated fitness device is needed. Open multiple tabs/browsers to simulate multiple players.
- Local infra runs on Kubernetes (`kind`) for the whole stack, genuinely practicing the "exposure to... Kubernetes" line in the JD; Docker Compose remains the everyday local dev loop for fast iteration (§11)

**Out of scope (skip to keep the project achievable):**

- Real hardware, firmware, Bluetooth
- Payments, e-commerce, video/class content CMS
- A real mobile app (a simulated web client is enough)
- Social login, complex multi-tenancy

## 2. High-level architecture

Three cooperating Go binaries make up the backend. Main flow:

1. Client (mock FE) calls REST to log in, create/join a race → receives `race_id` + token.
2. Client opens a WebSocket to a **`ws-gateway` instance** (`cmd/ws-gateway`) — it terminates every WebSocket connection and reverse-proxies REST traffic too, so the frontend only ever talks to one address. `race-service` (`cmd/server`) never holds a client connection directly and is not reachable from outside the cluster.
3. `race-service` keeps room state in memory (one "room actor" goroutine per race, §4.1); it exchanges messages with a client over NATS (`internal/roomrelay`, one subject pair per race) rather than holding a socket itself, ticking periodically and broadcasting the new state to `ws-gateway`, which fans it out to every local connection for that room.
4. When a race finishes, `race-service` writes the results to PostgreSQL (transaction) and publishes to Kafka.
5. A separate `consumer` binary reads the Kafka events and batches them into PostgreSQL — **this project drops ClickHouse** (see §6), so there is no dedicated analytics store; the consumer's sink is the same Postgres database, just via a decoupled, asynchronous path instead of the room actor's own synchronous write.
6. Redis's job: (a) a lightweight "room → which `race-service` instance owns it" registry (`SET NX EX`, refreshed by heartbeat), letting any `ws-gateway` instance resolve where to route a room-scoped request; (b) a small pub/sub channel (`room:events`) that pushes ownership-change notifications into every `ws-gateway`'s own routing cache. Room state/telemetry itself travels over NATS (§5), not Redis.

## 3. Domain model & PostgreSQL schema

```sql
-- users
CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT UNIQUE NOT NULL,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- one race session (room)
CREATE TABLE races (
  id TEXT PRIMARY KEY,                   -- 12-char base58, generated in Go (internal/race.GenerateRaceID) —
                                          -- short enough to read aloud or type by hand to invite others
  name TEXT NOT NULL,
  distance_meters INT NOT NULL,          -- race target; for this project's typing race, the target word count (e.g. 1000) — name kept as-is, see §13
  prompt_text TEXT,                      -- the generated word text for this race; NULL until POST /races/{id}/start
  status TEXT NOT NULL DEFAULT 'pending', -- pending|active|finished|cancelled
  created_by UUID NOT NULL REFERENCES users(id),
  started_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_races_status_ended_at ON races (status, ended_at) WHERE status = 'finished'; -- supports the ranked/windowed leaderboard query

-- participants in a race + their final result
CREATE TABLE race_participants (
  race_id TEXT NOT NULL REFERENCES races(id),
  user_id UUID NOT NULL REFERENCES users(id),
  finish_rank INT,
  finish_time_ms BIGINT,
  avg_pace_watt NUMERIC,                 -- for the typing race: average words-per-minute — name kept as-is, see §13
  disconnected_count INT NOT NULL DEFAULT 0,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (race_id, user_id)
);

-- telemetry log, written in batches (not per tick, to avoid write amplification)
CREATE TABLE workout_samples (
  id BIGSERIAL PRIMARY KEY,
  race_id TEXT NOT NULL REFERENCES races(id),
  user_id UUID NOT NULL REFERENCES users(id),
  ts TIMESTAMPTZ NOT NULL,
  distance_m NUMERIC NOT NULL,           -- for the typing race: words typed correctly so far — name kept as-is, see §13
  pace_watt NUMERIC,                     -- for the typing race: current words-per-minute
  stroke_rate INT                        -- unused for the typing race
);
CREATE INDEX idx_workout_samples_race_user_ts ON workout_samples (race_id, user_id, ts);

-- aggregated leaderboard (materialized, refreshed periodically or via trigger)
CREATE TABLE leaderboard_alltime (
  user_id UUID PRIMARY KEY REFERENCES users(id),
  best_2000m_ms BIGINT,
  total_races INT NOT NULL DEFAULT 0,
  total_distance_m NUMERIC NOT NULL DEFAULT 0,
  total_wins INT NOT NULL DEFAULT 0,             -- races finished at rank 1
  total_pace_watt_sum NUMERIC NOT NULL DEFAULT 0, -- sum of each race's avg_pace_watt; divided by total_races at read time
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Points worth noting (in the spirit of "solid PostgreSQL": schema design, indexing, transactions):

- `workout_samples` will grow very fast → batch insert (`COPY` or multi-row insert every ~2-5s per client instead of every tick); consider time-based partitioning if data volume grows large.
- Finishing a race is one transaction: update `races.status`, write `race_participants` for every player, update `leaderboard_alltime` — all or nothing.
- Index according to real query patterns (leaderboard by race, by user), not indiscriminately — `idx_races_status_ended_at` is partial (`WHERE status = 'finished'`) since every row the windowed-leaderboard query scans is already `finished`; indexing pending/active/cancelled rows too would only add write overhead for zero read benefit.

## 4. Real-time multiplayer design (the core of what the JD targets)

### 4.1. The "room actor" pattern with a goroutine + channel

Each race room is one independent goroutine that owns all of that room's state (no shared memory across goroutines → avoids complex mutexes, avoids data races):

```go
type RoomActor struct {
    id           string
    participants map[string]*ParticipantState
    inbox        chan RoomEvent   // every input (join, telemetry, leave) goes through here
    broadcast    chan<- []byte    // sent out to connection writers
    ctx          context.Context
    cancel       context.CancelFunc
}

func (r *RoomActor) Run() {
    ticker := time.NewTicker(250 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case ev := <-r.inbox:
            r.applyEvent(ev)          // update state — no other goroutine ever touches this state
        case <-ticker.C:
            r.broadcastSnapshot()
        case <-r.ctx.Done():          // room is closing: race ended, or everyone left
            r.cleanup()
            return
        }
    }
}
```

`broadcast` feeds `internal/roomrelay`, which publishes onto NATS for whichever `ws-gateway` instance(s) hold connections for that race (§2, §5). The concurrency principles below apply identically on both sides of that process boundary, not just within one process:

- **Single-writer principle**: only `RoomActor.Run()` mutates state; every input (including from `ws-gateway`, arriving over NATS) must go through the `inbox` channel — never call the actor's fields directly from another goroutine.
- **Context for lifecycle**: each room and each client connection has its own `context.Context` with a clear parent-child relationship, so when a client disconnects its reader/writer goroutines are cancelled cleanly with no leaks.
- **Goroutine leaks**: each WebSocket connection (held by `ws-gateway`) has 2 goroutines (reader, writer) — make sure both exit when the connection closes (use `errgroup` or `sync.WaitGroup` + context cancellation, so one side failing doesn't leave the other blocked forever).
- **Data races**: run `go test -race` across all room-related tests, spanning `internal/room`, `internal/roomrelay`, `internal/roomlocator`, and `internal/wsgateway`; use sensibly sized buffered channels to avoid deadlocks between broadcast and a full inbox.
- **Backpressure**: if one client has a slow network, its writer goroutine must not block the entire room — `ws-gateway`'s per-connection buffered channel drops/logs when full, instead of letting the room actor stall.

### 4.2. WebSocket protocol (simple JSON for easy debugging; can switch to Protobuf later)

```text
Client -> Server: {"type":"join_race","race_id":"..."}
Client -> Server: {"type":"telemetry","seq":42,"distance_m":812.5,"pace_watt":210,"ts":...}
Server -> Client: {"type":"race_state","tick":1234,"participants":[{"user_id":"...","distance_m":...,"rank":1}, ...]}
Server -> Client: {"type":"race_finished","results":[...]}
```

`GET /ws` is served by `ws-gateway`. Internally, it forwards a decoded client frame to `race-service` wrapped in a small envelope (`internal/roomrelay.InboundEnvelope`) over NATS, and receives broadcasts back the same way (`OutboundEnvelope`) — an implementation detail invisible to the client.

- `seq` is a monotonically increasing counter set by the client → the server uses it to detect **message ordering / out-of-order / duplicate** messages (caused by retries after a dropped connection). The server only applies a sample if its `seq` is greater than the last sample it received for that participant.
- The server-side tick rate is decoupled from how often clients send data — separating "ingest rate" from "broadcast rate" for better load handling.
- For this project's typing race (§13), the client sends one `telemetry` message per word typed correctly, not on a fixed timer — `distance_m` is the running count of correct words, `pace_watt` is current WPM. This is naturally bounded by human typing speed (roughly 0.4–2s between messages), which fits neatly into the decoupled ingest/broadcast design above without any special-casing.

### 4.3. Reconnection

- Each participant gets a per-race `session_token` (distinct from the main JWT) for fast reconnection without re-logging in — verified by `ws-gateway` at connect time.
- On an abrupt WebSocket close: the room actor does **not** remove the participant immediately — it marks `disconnected_at` and keeps them in the room for up to N seconds (grace period, e.g. 30s).
- If the client reconnects within the grace period with the correct `session_token` → re-attach the connection to the existing state and resend a full snapshot so the client can resync.
- If the grace period expires without a reconnect → treat it as leaving the race, mark them evicted (a small Redis set, `internal/roomlocator.MarkEvicted`/`IsEvicted`, checked by `ws-gateway` before attempting to reconnect a socket), and notify the other participants.
- This is exactly what the JD calls "keeping client state in sync and handling reconnection" — write dedicated tests that simulate a mid-race disconnect.

## 5. Horizontal scaling

Once running ≥2 instances of each service:

- **NATS Core (no JetStream) carries real-time room state between `ws-gateway` and `race-service`** — one subject pair per race (`room.{race_id}.in`/`room.{race_id}.out`, `internal/roomrelay`). This is what answers "keeping client state in sync... across multiple instances" from the JD; Redis plays a narrower, supporting role instead (below).
- **Redis is a room-ownership registry**: `SET room:<id> instance:<id> NX EX 60`, refreshed periodically by the owning `race-service` instance (`internal/roomlocator`). A small pub/sub channel (`room:events`) notifies every `ws-gateway` instance when a room's ownership changes, feeding a local routing cache rather than being polled per-request.
- `ws-gateway` resolves **room-scoped** REST/WS routing by reading that registry (`roomlocator.Owner`). For **room-less** REST traffic (e.g. `POST /races`, `GET /races` — no specific race to route to yet), it round-robins across whatever `race-service` pods currently exist — discovered dynamically in-cluster via a Kubernetes `EndpointSlice` watch (`client-go`). A static instance list would go stale the moment `race-service` scales under its own `HorizontalPodAutoscaler` (§7) — a newly-spawned pod `ws-gateway` never learned about would be an unreachable, silent routing dead end — so live discovery is the mechanism, not a fixed list.
- This directly exercises "exposure to horizontally scaled real-time services, stateful sessions running across multiple instances" from the JD, with ≥2 replicas of both `race-service` and `ws-gateway` under Kubernetes (§7).

## 6. Event pipeline & leaderboard analytics

- When a race finishes (or periodically during the race), `race-service` publishes events to Kafka, split into two separate topics so consumers can scale independently and schemas don't get mixed up:
  - `workout.sample` — batched telemetry (distance, pace, stroke_rate) during the race
  - `race.finished` — the final result of the whole race (final rank, finish time)
- The message key is `race_id` for both topics, ensuring messages for the same race land in the same partition → preserving ordering within that race, matching the "message ordering" theme the JD mentions.
- **ClickHouse is dropped from this project.** A dedicated Go `consumer` binary reads both topics — as **two separate consumer groups**, one per topic, since sharing one group ID across readers subscribed to different topics silently starves one of them under Kafka's own partition-assignment protocol — and batch-inserts into PostgreSQL instead, specifically `workout_samples` (§3), a table that exists in the schema but is otherwise never written to. `race.finished` is handled as an **idempotent reconciliation write** (`ReconcileParticipantResults`), not a second primary writer of `race_participants`, since that table is already written synchronously and transactionally by the room actor the instant a race finishes (§4.1) — the async Kafka path exists to make `workout_samples` real and to decouple the room actor from a per-event Postgres write, not to duplicate state that's already correctly persisted. The ranked/windowed "top N this week/month" query pattern that would have motivated a columnar store like ClickHouse isn't part of this project's scope — §8's leaderboard endpoints stay simple SQL against Postgres, indexed for it (§3). Batch inserts, not row-by-row.
- A **Dead Letter Topic** (`workout.sample.dlq`/`race.finished.dlq`) exists for messages that fail to parse or fail to write for a permanent reason (e.g. a foreign-key violation) — the consumer commits its offset and moves on instead of crash-looping on an unprocessable message; only a transient failure is left uncommitted for Kafka's own redelivery.

## 7. Kubernetes for local development

The goal is to practice real K8s workflows, not run a production cluster. This project uses **kind** (Kubernetes in Docker) locally.

```text
deploy/k8s/
  namespace.yaml
  configmap.yaml
  secret.yaml
  postgres/            # StatefulSet + PVC + Service
  redis/                # Deployment + Service
  nats/                 # Deployment + Service — NATS Core, no JetStream
  kafka/                 # Bitnami Helm chart values — the one deliberate exception to "plain manifests" (see §11)
  race-service/
    statefulset.yaml    # stable per-pod DNS identity; internal only, no Ingress
    service.yaml         # headless
    hpa.yaml              # CPU-based HorizontalPodAutoscaler, minReplicas 2 / maxReplicas 5
  ws-gateway/
    deployment.yaml
    service.yaml
    ingress.yaml          # the external entry point — see §2, §8
    hpa.yaml              # CPU-based, same 2-5 range
    rbac.yaml             # ServiceAccount + Role for its own EndpointSlice watch (§5)
  consumer/
    deployment.yaml       # no Service — no HTTP surface today (Phase 6 gives it one for /metrics)
```

Things worth practicing here because they connect directly to the spirit of the JD ("investigate and resolve production issues", "horizontally scaled... stateful sessions"):

- **Multiple replicas for both `race-service` and `ws-gateway`**, each under its own CPU-based `HorizontalPodAutoscaler` (2-5 replicas) — forces real handling of the NATS/Redis cross-instance design in §5, not just a fixed replica count that never exposes sync bugs. `metrics-server` is a real prerequisite for this (`kind` needs a `--kubelet-insecure-tls` patch it doesn't ship with by default).
- **Readiness probe separate from liveness**, on both services: `/healthz` only reports ready once Postgres/Redis/NATS/Kafka (whichever a given binary depends on) are actually reachable; `/livez` is dependency-free, so a transient broker blip doesn't make `kubelet` restart an otherwise-healthy pod.
- **Graceful shutdown**, on both services: a pod receiving `SIGTERM` lets any in-progress work finish before it terminates — `race-service` waits for its own in-progress rooms to drain (`terminationGracePeriodSeconds: 150`), `ws-gateway` force-disconnects its local connections cleanly after a short flush window (`terminationGracePeriodSeconds: 30`) rather than cutting them off mid-write.
- **`deploy/kind-config.yaml` + the `ingress-nginx` controller**, both required for `Ingress` to actually be reachable from the host — a plain `kind create cluster` maps no host ports and installs no ingress controller.
- No complex CI/CD — `kind load docker-image` is enough for a side project.

## 8. API surface

**REST (public, for the client):**

- `POST /auth/register`, `POST /auth/login`
- `POST /races` — create a race
- `POST /races/{id}/join` — returns a WS session token
- `POST /races/{id}/start` — creator starts the race: generates the shared typing-race prompt text, flips status to `active` (§13)
- `GET /races/{id}/text` — fetch the race's already-generated prompt text (for players joining after start, or reconnecting)
- `GET /races/{id}` — status/results
- `GET /races` — browse open (pending, joinable) races
- `GET /leaderboard/me` — the caller's own all-time stats (races joined, races won, avg WPM)
- `GET /leaderboard?window=&page=` — ranked, windowed leaderboard (e.g. all-time/weekly), paginated

**WebSocket:** `GET /ws?race_id=...&session_token=...` — served by `ws-gateway` (§2, §4.2).

A separate gateway process (`ws-gateway`, `cmd/ws-gateway`) sits in front of `race-service`. It carries no domain/business logic of its own: every REST request above (all except the WebSocket upgrade itself) is decoded, handled, and answered by `race-service` — `ws-gateway` reverse-proxies to whichever instance owns the answer (§5) rather than reimplementing any of it. No internal gRPC service (dropped from this project's scope — no real consumer, since the frontend already gets live rankings over the WebSocket above, and a browser can't reach raw gRPC without infrastructure this project isn't adding) and no separate Analytics/Leaderboard service (leaderboard stays `race-service`'s own package and endpoints).

## 9. Observability & production-style operations

- Structured logging (`slog`), tagged with `race_id`/`user_id`/`request_id`, wired in all three binaries.
- Prometheus metrics: `race-service` exposes active room count, broadcast tick latency, goroutine count (via the standard Go runtime collector), and channel buffer usage; `ws-gateway`/`consumer` have no Prometheus wiring yet — closing that gap, along with metrics tied to what they actually depend on (NATS, Redis, Kafka), is Phase 6's `metrics/metrics-parity.md`.
- `pprof` enabled on `race-service` (`PPROF_ENABLED`); Phase 6 extends it to `ws-gateway`/`consumer`.
- Phase 6 builds full-depth distributed tracing (REST/WebSocket entry points, NATS, Redis, Kafka, `pgx`, including a span per `telemetry` message) against Tempo, plus Grafana as the correlation layer across metrics/traces/logs, centralized logging (EFK), and Alertmanager-driven alerting routed to Telegram — see `context/features/phase6/phase-6-plan.md` for the full design and the concrete decisions behind each piece.

## 10. Testing strategy

- Unit tests for the logic that applies events to room state (pure functions, easy to test).
- `go test -race ./...` mandatory for every concurrency-related package, including `internal/room`, `internal/roomrelay`, `internal/roomlocator`, `internal/wsgateway`, and `internal/consumer`.
- Simulation tests: N simulated clients sending telemetry concurrently, one client disconnecting mid-race then reconnecting, verifying the final state is correct.
- Load testing with [k6](https://k6.io/) (supports WebSocket) — `load/scenarios/` holds this project's scenarios, run against either Docker Compose or the in-cluster `Ingress`.

## 11. Tech stack

- Go 1.26, `net/http` + [`coder/websocket`](https://github.com/coder/websocket) (the actively maintained successor to `nhooyr.io/websocket`, same original author).
- PostgreSQL 18 (`postgres:18-alpine`), `pgx/v5` as the driver, raw SQL + `golang-migrate` for schema migrations (no ORM, no `sqlc` — hand-written SQL, to actually practice it).
- Redis 7 — a room-ownership registry and a small ownership-change pub/sub channel (§5).
- NATS 2 (NATS Core, no JetStream) — the real-time room-state transport between `ws-gateway` and `race-service` (`nats.go` client).
- Kafka via `segmentio/kafka-go`; in-cluster via the Bitnami Helm chart (`bitnamilegacy/kafka` image — the chart's own default image requires a paid Bitnami subscription and 404s on a real pull; `bitnamilegacy` is the free community mirror) — the one deliberate exception to this project's "plain manifests over an operator/chart" stance elsewhere. No ClickHouse — dropped from this project entirely (§6); the consumer's sink is PostgreSQL.
- Local Kubernetes: **kind** (not minikube), via `deploy/kind-config.yaml` (host port mapping + `ingress-ready` node label) and a separately-installed `ingress-nginx` controller — both required for `Ingress` to work at all (§7). Docker Compose remains the everyday local dev loop for fast iteration.
- Frontend: React (Vite), styled with Tailwind CSS (v4, CSS-based `@theme` config — see `context/coding-standards.md`) and shadcn/ui for form/button primitives, using the browser's native WebSocket API directly against `ws-gateway`'s address; open multiple tabs/browsers to simulate multiple players.

## 12. Phased roadmap (mapped directly to the JD)

### Phase 1 — Foundation (JD: "write reliable Go", "REST APIs", basic PostgreSQL)

- Auth, create/join a race over REST, basic Postgres schema, a simple race flow (single instance, no Redis needed yet).
- Minimal React app (login, create/join race via REST), scaffolded with Tailwind CSS + shadcn/ui — run Postgres via `docker compose`, no K8s needed at this phase.

### Phase 2 — Real-time core (JD: goroutines/channels/context, reconnection, concurrency pitfalls)

- Room actor pattern, WebSocket, tick-based broadcast, reconnect handling + grace period, race-detector tests + simulated disconnects.
- React app gains a WebSocket client, showing participants' positions updating in real time.

### Phase 3 — Production-readiness (JD: "investigate and resolve production issues", metrics/logs)

- Structured logging, Prometheus metrics, pprof, load testing, fixing backpressure/goroutine leaks uncovered by load testing.

### Phase 4 — Horizontal scale + event pipeline (JD: Redis, Kafka, NATS)

- Run multiple instances of `race-service` and `ws-gateway`; NATS carries real-time room state between them (`internal/roomrelay`), and Redis provides a lightweight room-ownership registry (`internal/roomlocator`) so any `ws-gateway` instance can resolve which `race-service` instance owns a given room (§5).
- Kafka event pipeline for telemetry/results: `workout.sample`/`race.finished`, two separate consumer groups, a dead-letter topic (§6). No ClickHouse and no internal gRPC service (see §6 and §8 for why).
- See `context/features/phase4/phase-4-plan.md` for the detailed spec breakdown.

### Phase 5 — Kubernetes (JD: exposure to Kubernetes)

- The entire stack (Postgres, Redis, NATS, Kafka, `race-service`, `ws-gateway`, `consumer`) runs on local Kubernetes (`kind`) — multiple replicas of `race-service` and `ws-gateway`, each under a CPU-based `HorizontalPodAutoscaler`, with rolling updates and graceful shutdown that don't drop an in-progress race (§7). `ws-gateway` discovers `race-service` pods dynamically via a Kubernetes `EndpointSlice` watch rather than a static list, which is what makes scaling `race-service` under its own `HorizontalPodAutoscaler` safe at all (§5).
- See `context/features/phase5/phase-5-plan.md`.

### Phase 6 — Observability (JD: "investigate and resolve production issues")

- A full observability stack: closing the Prometheus-metrics gap on `ws-gateway`/`consumer`, full-depth distributed tracing (Tempo) across every hop (REST/WebSocket, NATS, Redis, Kafka, `pgx`), centralized logs (EFK) correlated with traces, Grafana as the single pane of glass, and Alertmanager-driven alerting routed to Telegram via a small purpose-built adapter (§9).
- See `context/features/phase6/phase-6-plan.md` for the full design.

Work through the phases in order — don't jump straight ahead. Phase 2 is what the JD values most (concurrency + real-time consistency), so it should get the most testing attention. Kafka, NATS, and Kubernetes only pay off once there are actually ≥2 instances that need to stay in sync — doing them too early risks turning into infrastructure overhead rather than Go practice.

## 13. This project's race mechanic: typing race

Rather than simulating real fitness-device telemetry in the browser (awkward to fake convincingly), this project's "workout" is a **typing race**: players race cars whose position is driven by how many words they type correctly against a shared prompt, generated fresh per race.

- `POST /races/{id}/start` (creator-only) generates a random word string server-side and stores it on the race row (`races.prompt_text`); flips `status` to `active`. `GET /races/{id}/text` lets any participant fetch that same text — needed for players who load in after start, or reconnect mid-race.
- The client sends one `telemetry` message (§4.2) per word typed correctly, not on a fixed timer — this is naturally bounded by human typing speed (roughly 0.4–2s between messages even for a fast typist), which is neither too chatty for the server nor too sparse for a responsive race; the room actor's decoupled 250ms broadcast tick keeps everyone else's view smooth regardless of exactly when a given player's message arrives.
- The server never inspects what a player actually typed — it trusts the client-reported progress the same way it would trust a real rowing machine's reported distance, i.e. no server-side text verification. This keeps the trust model identical to the original fitness-telemetry design.
- Existing schema/protocol field names are deliberately reused rather than renamed for this mechanic (`distance_meters` is the target word count, `distance_m` is words-typed-correctly, `pace_watt` is WPM, `stroke_rate` is unused) — avoids schema churn since the underlying real-time architecture doesn't care what the numbers represent.
- Everything else in this document — the room actor pattern (§4.1), reconnection (§4.3), horizontal scaling (§5), event pipeline (§6), Kubernetes (§7), observability (§9), testing strategy (§10) — applies unchanged; only the meaning of the telemetry signal changes.
