# Current Feature: Project Scaffolding & Local Postgres

## Status

Not Started

## Goals

- `go build ./...` succeeds from `backend/` (module `aviron/backend`, Go 1.22)
- `docker compose up -d postgres` starts Postgres 16 on `localhost:5432`; `go run ./cmd/server` applies pending migrations on startup
- `GET /healthz` returns `200 {"status":"ok"}` when `pool.Ping(ctx)` succeeds, `503 {"status":"db_unreachable"}` otherwise
- `go test ./... -race` passes
- `make run` starts the server, `make test` runs the suite

## Explain

- Request/startup flow: `main.go` loads `config.Load()` from env → opens a `pgxpool.Pool` via `db.NewPool` → runs `db.Migrate` (golang-migrate) → builds the mux via `httpserver.NewServer(pool)` → `http.ListenAndServe(":"+cfg.Port, mux)`
- `/healthz` is the only endpoint in this feature; it exists purely to prove the DB connection works end-to-end before any real feature (auth, races) is built on top
- Migrations are versioned `.sql` files under `migrations/`, applied via `golang-migrate`, not a single init script — later features add new numbered migrations instead of editing this one
- Out of scope: auth endpoints, race create/join endpoints, WebSocket/room actor (Phase 2), Redis/Kafka/Kubernetes (later phases), frontend scaffolding (its own feature)

## Plan

### Directory layout

```text
backend/
  go.mod
  go.sum
  Makefile
  cmd/
    server/
      main.go
  internal/
    config/
      config.go        # Config struct + Load()
    db/
      db.go             # NewPool(ctx, dsn) (*pgxpool.Pool, error)
      migrate.go         # Migrate(dsn string) error
    httpserver/
      server.go          # NewServer(pool *pgxpool.Pool) http.Handler
      healthz.go          # healthzHandler(pool *pgxpool.Pool) http.HandlerFunc
      healthz_test.go
  migrations/
    000001_init_schema.up.sql
    000001_init_schema.down.sql
docker-compose.yml
```

### `internal/config`

```go
type Config struct {
    DatabaseURL string
    Port        string
}

func Load() Config {
    return Config{
        DatabaseURL: getEnv("DATABASE_URL", "postgres://aviron:aviron@localhost:5432/aviron?sslmode=disable"),
        Port:        getEnv("PORT", "8080"),
    }
}
```

### `internal/db`

- `NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)` — thin wrapper over `pgxpool.New`
- `Migrate(dsn string) error` — wraps `github.com/golang-migrate/migrate/v4` with `source: file://migrations`, `database: postgres`
- `000001_init_schema.up.sql` creates the five tables + index from context/project-overview.md §3 (`users`, `races`, `race_participants`, `workout_samples`, `leaderboard_alltime`); `000001_init_schema.down.sql` drops them in reverse order

### `internal/httpserver`

```go
func NewServer(pool *pgxpool.Pool) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /healthz", healthzHandler(pool))
    return mux
}

func healthzHandler(pool *pgxpool.Pool) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
        defer cancel()
        w.Header().Set("Content-Type", "application/json")
        if err := pool.Ping(ctx); err != nil {
            w.WriteHeader(http.StatusServiceUnavailable)
            json.NewEncoder(w).Encode(map[string]string{"status": "db_unreachable"})
            return
        }
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
    }
}
```

Uses Go 1.22's method-aware `http.ServeMux` patterns (`"GET /healthz"`) — no router dependency needed yet.

### `cmd/server/main.go`

```go
func main() {
    cfg := config.Load()
    ctx := context.Background()

    pool, err := db.NewPool(ctx, cfg.DatabaseURL)
    if err != nil {
        log.Fatalf("db: %v", err)
    }
    defer pool.Close()

    if err := db.Migrate(cfg.DatabaseURL); err != nil {
        log.Fatalf("migrate: %v", err)
    }

    srv := httpserver.NewServer(pool)
    log.Printf("listening on :%s", cfg.Port)
    log.Fatal(http.ListenAndServe(":"+cfg.Port, srv))
}
```

### `docker-compose.yml`

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: aviron
      POSTGRES_PASSWORD: aviron
      POSTGRES_DB: aviron
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
```

### `Makefile`

```makefile
run:
    go run ./cmd/server

test:
    go test ./... -race
```

### Tests

- `internal/httpserver/healthz_test.go`: `TestHealthz_OK` spins up `httptest.NewServer(NewServer(pool))` against a real test DB and asserts `200` + `{"status":"ok"}`
- Stretch: `TestHealthz_DBUnreachable` against a pool pointed at an unreachable DSN, asserting `503` — only if faking a dead pool cleanly is easy with `pgxpool`; otherwise skip and note it as a gap

No divergence from context/project-overview.md's suggested tech stack (§11) — `pgx`/`pgxpool`, Docker Compose for Phase 1, no ORM.

## Notes

- Repo is not yet a git repository (`git init` needed) — required before `/feature start` can create a branch
- Module path is decided as `aviron/backend` (short local path, not a publishable URL) since there's no remote yet; revisit if this repo gets pushed somewhere and imports need a real path
- "Done" means: build green, tests green, `/healthz` returns 200 against local Postgres
- Next feature per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`)

## History
