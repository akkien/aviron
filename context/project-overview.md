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
- Per-user all-time stats (races joined, races won, avg WPM), queried from Postgres, backing the dashboard's own stat cards
- Horizontal scaling: run ≥2 Go instances, use Redis pub/sub to sync room state cross-instance
- Observability: structured logs, Prometheus metrics, optionally OpenTelemetry tracing
- Testing: unit tests for logic, `go test -race` for concurrency, load testing with k6/ghz
- Frontend: a small React app (Vite), styled with Tailwind CSS and shadcn/ui components (speeds up building consistent forms/buttons without hand-rolled CSS — still no polish investment, per the scope note above) — login screen, create/join race, and a typing-race view (type a shared prompt to move your car) showing participants' positions updating in real time over WebSocket. Real typing input doubles as the "device telemetry," so no simulated fitness device is needed. Open multiple tabs/browsers to simulate multiple players.
- Local infra runs on Kubernetes (kind or minikube) instead of just Docker Compose, to genuinely practice the "exposure to... Kubernetes" line in the JD.

**Out of scope (skip to keep the project achievable):**

- Real hardware, firmware, Bluetooth
- Payments, e-commerce, video/class content CMS
- A real mobile app (a simulated web client is enough)
- Social login, complex multi-tenancy

## 2. High-level architecture

See the diagram above. Main flow:

1. Client (mock FE) calls REST to log in, create/join a race → receives `race_id` + token.
2. Client opens a WebSocket to the API Gateway; the gateway forwards it to the **Race Service instance** currently holding that room (or routes via Redis if the room lives on a different instance).
3. The Race Service keeps room state in memory (one "room actor" goroutine per race), receives telemetry input from each client, ticks periodically, and broadcasts the new state to every client in the room.
4. When a race finishes, the Race Service writes the results to PostgreSQL (transaction) and emits an event to Kafka/NATS.
5. A separate consumer reads the events and loads them into ClickHouse to serve large-scale leaderboard/analytics queries (this is the "strong plus" part, done after the MVP is stable).
6. Redis is used for two things: (a) pub/sub so instances stay in sync when a room has clients on different instances, (b) storing "room → which instance owns it" as a lightweight service registry.

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
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  distance_meters INT NOT NULL,          -- race target; for this project's typing race, the target word count (e.g. 1000) — name kept as-is, see §13
  prompt_text TEXT,                      -- the generated word text for this race; NULL until POST /races/{id}/start
  status TEXT NOT NULL DEFAULT 'pending', -- pending|active|finished|cancelled
  created_by UUID NOT NULL REFERENCES users(id),
  started_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- participants in a race + their final result
CREATE TABLE race_participants (
  race_id UUID NOT NULL REFERENCES races(id),
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
  race_id UUID NOT NULL REFERENCES races(id),
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
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Points worth noting (in the spirit of "solid PostgreSQL": schema design, indexing, transactions):

- `workout_samples` will grow very fast → batch insert (`COPY` or multi-row insert every ~2-5s per client instead of every tick); consider time-based partitioning if data volume grows large.
- Finishing a race is one transaction: update `races.status`, write `race_participants` for every player, update `leaderboard_alltime` — all or nothing.
- Index according to real query patterns (leaderboard by race, by user), not indiscriminately.

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

Concurrency principles to practice properly, as the JD requires:

- **Single-writer principle**: only `RoomActor.Run()` mutates state; every input (including from the WebSocket reader goroutine) must go through the `inbox` channel — never call the actor's fields directly from another goroutine.
- **Context for lifecycle**: each room and each client connection has its own `context.Context` with a clear parent-child relationship, so when a client disconnects its reader/writer goroutines are cancelled cleanly with no leaks.
- **Goroutine leaks**: each WebSocket connection has 2 goroutines (reader, writer) — make sure both exit when the connection closes (use `errgroup` or `sync.WaitGroup` + context cancellation, so one side failing doesn't leave the other blocked forever).
- **Data races**: run `go test -race` across all room-related tests; use sensibly sized buffered channels to avoid deadlocks between broadcast and a full inbox.
- **Backpressure**: if one client has a slow network, its writer goroutine must not block the entire room — use a per-connection buffered channel that drops/logs when full, instead of letting the room actor stall.

### 4.2. WebSocket protocol (simple JSON for easy debugging; can switch to Protobuf later)

```text
Client -> Server: {"type":"join_race","race_id":"..."}
Client -> Server: {"type":"telemetry","seq":42,"distance_m":812.5,"pace_watt":210,"ts":...}
Server -> Client: {"type":"race_state","tick":1234,"participants":[{"user_id":"...","distance_m":...,"rank":1}, ...]}
Server -> Client: {"type":"race_finished","results":[...]}
```

- `seq` is a monotonically increasing counter set by the client → the server uses it to detect **message ordering / out-of-order / duplicate** messages (caused by retries after a dropped connection). The server only applies a sample if its `seq` is greater than the last sample it received for that participant.
- The server-side tick rate is decoupled from how often clients send data — separating "ingest rate" from "broadcast rate" for better load handling.
- For this project's typing race (§13), the client sends one `telemetry` message per word typed correctly, not on a fixed timer — `distance_m` is the running count of correct words, `pace_watt` is current WPM. This is naturally bounded by human typing speed (roughly 0.4–2s between messages), which fits neatly into the decoupled ingest/broadcast design above without any special-casing.

### 4.3. Reconnection

- Each participant gets a per-race `session_token` (distinct from the main JWT) for fast reconnection without re-logging in.
- On an abrupt WebSocket close: the room actor does **not** remove the participant immediately — it marks `disconnected_at` and keeps them in the room for up to N seconds (grace period, e.g. 30s).
- If the client reconnects within the grace period with the correct `session_token` → re-attach the connection to the existing state and resend a full snapshot so the client can resync.
- If the grace period expires without a reconnect → treat it as leaving the race and notify the other participants.
- This is exactly what the JD calls "keeping client state in sync and handling reconnection" — write dedicated tests that simulate a mid-race disconnect.

## 5. Horizontal scaling (strong plus, do this after the MVP is working)

Once running ≥2 Go instances:

- You need a way to know **which instance is running which room** — use Redis (`SET room:<id> instance:<id> NX EX 60`, refreshed periodically) as a simple registry; no need for Kafka here.
- When joining a room, the API Gateway (or client) must be routed to the correct instance holding that room (sticky routing), or more simply: every instance publishes/subscribes to a Redis pub/sub channel keyed by `room_id`, so a client can connect to any instance and that instance just forwards messages via Redis to whichever instance currently "owns" the room.
- This directly exercises "exposure to horizontally scaled real-time services, stateful sessions running across multiple instances" from the JD — do it after the single-instance version is solid; doing it too early risks getting bogged down.

## 6. Event pipeline & leaderboard analytics (strong plus)

- When a race finishes (or periodically during the race), the Race Service publishes events to Kafka, split into two separate topics so consumers can scale independently and schemas don't get mixed up:
  - `workout.sample` — batched telemetry (distance, pace, stroke_rate) during the race
  - `race.finished` — the final result of the whole race (final rank, finish time)
- The message key should be `race_id` or `user_id` (depending on the query pattern you want to optimize for) to ensure messages for the same entity land in the same partition → preserving ordering within that entity, matching the "message ordering" theme the JD mentions.
- A dedicated Go consumer group reads both topics and writes into ClickHouse (a wide table optimized for queries like "top N this week/month", "personal PR"). Use batch inserts into ClickHouse rather than row-by-row inserts.
- Consider adding a Dead Letter Topic (`*.dlq`) for messages that fail to parse/write — good practice for handling pipeline errors instead of letting the consumer crash-loop.
- This is an advanced step, done **after** the core multiplayer + Postgres piece is solid — the JD lists it under "strong plus," not must-have.

## 7. Kubernetes for local development (strong plus)

The goal is to practice real K8s workflows, not run a production cluster. Use **kind** (Kubernetes in Docker) or **minikube** locally.

Suggested manifest layout (or package as a Helm chart if you also want to practice Helm):

```text
deploy/k8s/
  namespace.yaml
  postgres/            # StatefulSet + PVC + Service (or use the Bitnami Helm chart)
  redis/                # Deployment + Service
  kafka/                # use an existing chart (Strimzi or Bitnami) rather than writing your own
  race-service/
    deployment.yaml     # readiness/liveness probes, resource limits
    service.yaml
    hpa.yaml             # HorizontalPodAutoscaler on CPU or a custom metric (connection count)
  api-gateway/
  configmap.yaml
  secret.yaml
```

Things worth practicing here because they connect directly to the spirit of the JD ("investigate and resolve production issues", "horizontally scaled... stateful sessions"):

- **Multiple replicas for race-service**: run `replicas: 2` to force yourself to handle the Redis cross-instance pub/sub designed in section 5 correctly — a single pod never exposes sync bugs.
- **Readiness probe separate from liveness**: race-service should only be "ready" once it's connected to Postgres/Redis/Kafka, to avoid traffic being routed to a pod that isn't ready yet.
- **Graceful shutdown**: a pod receiving `SIGTERM` (sent by K8s on scale-down/rolling update) must close its active room actors properly, without cutting off the WebSocket of someone mid-race — a good place to practice `context` cancellation at the whole-service level.
- **Port-forward / Ingress (nginx-ingress or Traefik)** to expose the API Gateway outside the cluster so the React app running on the host machine can reach it.
- No need for a complex CI/CD setup for a side project — `kind load docker-image` or building straight into the local cluster is enough.

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

**WebSocket:** `GET /ws?race_id=...&session_token=...`

**gRPC (internal, optional — for the "gRPC is a plus" line):** communication between the Race Service and a separate Analytics/Leaderboard service, e.g. `GetLiveRankings(race_id) returns (stream RankingUpdate)`.

## 9. Observability & production-style operations

- Structured logging (`slog`), always tagged with `race_id`, `user_id`, `request_id`.
- Prometheus metrics: active room count, connection count, broadcast tick latency, goroutine count, channel buffer usage (direct visibility into goroutine/memory leaks — right in line with the JD).
- `pprof` enabled in dev/staging environments to inspect real goroutine leaks.
- OpenTelemetry tracing across REST + the join-race flow — useful for "debugging production services with logs and metrics."

## 10. Testing strategy

- Unit tests for the logic that applies events to room state (pure functions, easy to test).
- `go test -race ./...` mandatory for every concurrency-related package.
- Simulation tests: N simulated clients sending telemetry concurrently, one client disconnecting mid-race then reconnecting, verifying the final state is correct.
- Load testing with [k6](https://k6.io/) (supports WebSocket) or a hand-written Go load-test client that opens hundreds of simulated connections.

## 11. Suggested tech stack

- Go 1.22+, `net/http` + `gorilla/websocket` or `nhooyr.io/websocket`
- PostgreSQL 16, `pgx` as the driver, `sqlc` or raw SQL (avoid a heavy ORM to actually practice SQL)
- Redis 7 (pub/sub + registry)
- Kafka (using `segmentio/kafka-go` or `confluent-kafka-go`); run locally via the Strimzi operator or the Bitnami Kafka Helm chart on K8s — avoid manually managing Zookeeper/brokers yourself
- ClickHouse (final phase, optional)
- Local Kubernetes (kind or minikube) for the whole stack (Postgres, Redis, Kafka, race-service, api-gateway); Docker Compose is only a temporary stand-in for Phase 1 while the code doesn't yet need multiple instances
- Frontend: React (Vite), styled with Tailwind CSS (v4, CSS-based `@theme` config — see context/coding-standards.md) and shadcn/ui for form/button primitives, using the browser's native WebSocket API directly (no need for a heavy real-time library yet); open multiple tabs/browsers to simulate multiple players.

## 12. Phased roadmap (mapped directly to the JD)

### Phase 1 — Foundation (JD: "write reliable Go", "REST APIs", basic PostgreSQL)

- Auth, create/join a race over REST, basic Postgres schema, a simple race flow (single instance, no Redis needed yet).
- Minimal React app (login, create/join race via REST), scaffolded with Tailwind CSS + shadcn/ui — run Postgres via `docker compose`, no K8s needed at this phase.

### Phase 2 — Real-time core (JD: goroutines/channels/context, reconnection, concurrency pitfalls)

- Room actor pattern, WebSocket, tick-based broadcast, reconnect handling + grace period, race-detector tests + simulated disconnects.
- React app gains a WebSocket client, showing participants' positions updating in real time (good enough for visual manual testing, doesn't need to be polished).

### Phase 3 — Production-readiness (JD: "investigate and resolve production issues", metrics/logs)

- Structured logging, Prometheus metrics, pprof, load testing, fixing backpressure/goroutine leaks uncovered by load testing.

### Phase 4 — Strong plus (JD: horizontal scale, Redis, Kafka, ClickHouse, gRPC, Kubernetes)

- Run multiple instances + Redis pub/sub for cross-instance sync.
- Add a Kafka → ClickHouse event pipeline for the leaderboard, add an internal gRPC service.
- Move the entire stack (Postgres, Redis, Kafka, race-service, api-gateway) onto local Kubernetes (kind/minikube) — this is when multi-instance behavior actually becomes meaningful to test (HPA, rolling updates that don't drop active WebSocket connections, etc.).

Work through the phases in order — don't jump straight to Phase 4. Phase 2 is what the JD values most (concurrency + real-time consistency), so spend the most time there and write plenty of tests for it. Kafka and Kubernetes only pay off once you actually have ≥2 instances that need to stay in sync — doing them earlier tends to turn into infrastructure overhead rather than Go practice.

## 13. This project's race mechanic: typing race

Rather than simulating real fitness-device telemetry in the browser (awkward to fake convincingly), this project's "workout" is a **typing race**: players race cars whose position is driven by how many words they type correctly against a shared prompt, generated fresh per race.

- `POST /races/{id}/start` (creator-only) generates a random word string server-side and stores it on the race row (`races.prompt_text`); flips `status` to `active`. `GET /races/{id}/text` lets any participant fetch that same text — needed for players who load in after start, or reconnect mid-race.
- The client sends one `telemetry` message (§4.2) per word typed correctly, not on a fixed timer — this is naturally bounded by human typing speed (roughly 0.4–2s between messages even for a fast typist), which is neither too chatty for the server nor too sparse for a responsive race; the room actor's decoupled 250ms broadcast tick keeps everyone else's view smooth regardless of exactly when a given player's message arrives.
- The server never inspects what a player actually typed — it trusts the client-reported progress the same way it would trust a real rowing machine's reported distance, i.e. no server-side text verification. This keeps the trust model identical to the original fitness-telemetry design.
- Existing schema/protocol field names are deliberately reused rather than renamed for this mechanic (`distance_meters` is the target word count, `distance_m` is words-typed-correctly, `pace_watt` is WPM, `stroke_rate` is unused) — avoids schema churn since the underlying real-time architecture doesn't care what the numbers represent.
- Everything else in this document — the room actor pattern (§4.1), reconnection (§4.3), horizontal scaling (§5), event pipeline (§6), Kubernetes (§7), observability (§9), testing strategy (§10) — applies unchanged; only the meaning of the telemetry signal changed.
