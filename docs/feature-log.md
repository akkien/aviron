# Feature Log

## Project Scaffolding & Local Postgres

Stood up the initial Go backend skeleton — module, package layout, local Postgres via Docker Compose, the initial schema migration, and a `/healthz` endpoint — so every later feature has a working build/test/run loop to land on.

### Goals

- `go build ./...` succeeds from `backend/` (module `github.com/akkien/aviron`)
- `docker compose up -d postgres` starts Postgres 18-alpine on `localhost:5432`; `go run ./cmd/server` applies pending migrations on startup
- `GET /healthz` returns `200 {"status":"ok"}` when `pool.Ping(ctx)` succeeds, `503 {"status":"db_unreachable"}` otherwise
- `go test ./... -race` passes
- `make run` starts the server, `make test` runs the suite

### Explain

- Startup flow: `main.go` loads `config.Load()` from env → opens a `pgxpool.Pool` via `db.NewPool` → runs `db.Migrate` (golang-migrate, using the `pgx/v5` driver over `database/sql`) → builds the mux via `httpserver.NewServer(pool)` → `http.ListenAndServe`
- `/healthz` is the only endpoint in this feature; it exists purely to prove the DB connection works end-to-end before any real feature (auth, races) is built on top
- Migrations are versioned `.sql` files under `migrations/`, applied via `golang-migrate`, not a single init script — later features add new numbered migrations instead of editing this one
- Routing uses Go 1.22's method-aware `http.ServeMux` (`"GET /healthz"`) — no router dependency needed yet
- Tests hit a real Postgres rather than a mock: `TestHealthz_OK` skips (not fails) if no DB is reachable, so `go test ./...` still works without Docker running; `TestHealthz_DBUnreachable` points at `localhost:1` (nothing listens there) to exercise the failure path without needing to fake a broken pool
- Trade-off: used `postgres:18-alpine` instead of the "PostgreSQL 16" originally suggested in context/project-overview.md §11 — newer, smaller image, no schema-relevant difference. This surfaced one gotcha worth remembering: the 18+ image expects its volume mounted at `/var/lib/postgresql`, not `/var/lib/postgresql/data` — the old mount point makes the container refuse to start, thinking it's an unmigrated pre-18 data directory
- Module path (`github.com/akkien/aviron`) was chosen to match the `origin` git remote once it turned out this repo already had git initialized (an earlier assumption that it didn't was wrong)
