# Project Scaffolding & Local Postgres

## Overview

Before any real feature (auth, races) can be built, the backend needs a working skeleton: a Go module that builds, a local Postgres database seeded with the initial schema, and one endpoint that proves the server can actually reach the database. This feature has no user-facing behavior — it's the foundation every later feature stands on.

## Requirements

### Project Layout

- `backend/` becomes a Go module (`go.mod`)
- `cmd/server/main.go` — one entrypoint that starts the HTTP server
- `internal/config` — reads `DATABASE_URL` and `PORT` from environment variables, with sane defaults for local dev
- `internal/db` — opens a `pgx` connection pool and runs pending migrations on startup
- `internal/httpserver` — the router and handlers

### Local Postgres

- `docker-compose.yml` at the repo root starts a single Postgres 16 container
- Migrations live as versioned `.sql` files (via `golang-migrate`) so later features add to them incrementally, instead of one big init script
- First migration creates the five tables from context/project-overview.md §3 — `users`, `races`, `race_participants`, `workout_samples`, `leaderboard_alltime` — with the indexes already specified there

### Health Check

- `GET /healthz` pings the database pool and returns:
  - `200 OK` with body `{"status":"ok"}` if the ping succeeds
  - `503 Service Unavailable` with body `{"status":"db_unreachable"}` if it doesn't
- No auth required on this endpoint

### Build & Test

- `go build ./...` succeeds from `backend/`
- `go test ./...` succeeds, including a test for `/healthz` that spins up the handler with `httptest` and hits it against a real test database
- `make run` starts the server locally; `make test` runs the test suite — standardize on these two names now so every later feature reuses them

## Non-Goals

- Auth endpoints, race create/join endpoints — next features
- WebSocket / room actor, ticking, reconnection (Phase 2)
- Redis, Kafka, Kubernetes (later phases)
- Frontend scaffolding — its own feature, so this one stays backend-only

## Notes

- Module path (e.g. `github.com/<username>/aviron/backend`) isn't decided yet — pick one during implementation, it doesn't block anything else here.
- "Done" means: build green, tests green, `/healthz` returns 200 against local Postgres.
- Once this merges, the next feature per the roadmap (context/project-overview.md §12, Phase 1) is auth.
