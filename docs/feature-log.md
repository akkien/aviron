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

## User Registration

Added `POST /auth/register`, introducing the layered `Handler → Service → Repository` architecture every backend domain now follows.

### Goals (User Registration)

- `POST /auth/register` → `201` with `{id, email, display_name}`, password never in the response
- `409 email_taken` on duplicate email; `400` field-keyed errors on invalid input
- Passwords hashed with bcrypt (cost 12), never logged

### Explain (User Registration)

- `Repository` is an interface owned by the service; `internal/postgres` provides the concrete implementation and translates Postgres errors (unique-violation) into domain sentinel errors like `ErrEmailTaken`
- Tests run against a fake in-memory repository — no real Postgres needed for handler/service tests

## Login & JWT Issuance

Added `POST /auth/login`, exchanging email and password for a signed JWT that later features verify.

### Goals (Login & JWT Issuance)

- `POST /auth/login` → `200` with `{token, expires_at}` for correct credentials
- `401 invalid_credentials` for wrong password or unknown email — identical response either way, so it can't be used to enumerate accounts
- JWT signed HS256 with `sub`/`email`/`exp` claims, 24h expiry

### Explain (Login & JWT Issuance)

- "User not found" and "wrong password" both collapse to one `ErrInvalidCredentials`, following the same repo-boundary error-translation convention as registration (extended `Repository` with `GetUserByEmail`)
- Renamed the domain's types to `AuthHandler`/`AuthService`/`AuthRepository` (matching `postgres.AuthRepository`'s existing naming) and consolidated all request/response DTOs into one `dtos.go` — now a standing convention for every domain

## JWT Auth Middleware

Added a reusable middleware that verifies the JWT `Login` issues and exposes the caller's user id to downstream handlers.

### Goals (JWT Auth Middleware)

- `Auth(jwtSecret []byte) func(http.Handler) http.Handler` passes valid tokens through; rejects everything else with `401 unauthorized`
- Downstream handlers read the authenticated user id via `UserIDFromContext`

### Explain (JWT Auth Middleware)

- Lives in its own `internal/middleware` package, not inside `internal/auth` — it has zero dependency on auth's domain types, only the raw secret and standard JWT claims, so it's a cross-cutting concern rather than part of the auth domain
- Not wired into any route yet — there's nothing to protect until the races endpoints exist
