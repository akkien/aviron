# Current Feature: User Registration

## Status

In Progress

## Goals

- `POST /auth/register` returns `201` with `{"id", "email", "display_name"}` for a valid new user — `password`/`password_hash` never appear in the response
- `409 {"error":"email_taken"}` when the email is already registered
- `400 {"errors":{...}}` (field-keyed) for invalid `email`, `password`, or `display_name`
- Passwords are stored as a bcrypt hash (cost 12) in `users.password_hash`, never logged
- `go test ./... -race` passes, including new tests for the handler: valid registration, duplicate email, each validation failure

## Explain

- First real domain endpoint beyond `/healthz` — introduces the `internal/auth` package and, with it, the layered route → handler → service → repository architecture now documented in context/coding-standards.md ("Backend Architecture"). Every backend feature after this one follows the same shape.
- Public endpoint, no JWT required (there's no token to require yet — this is how an account is created in the first place)
- Establishes the bcrypt password-hashing convention that `auth/login` (next feature) reuses to verify credentials
- `Repository` is an interface consumed by `auth.Service`; the concrete Postgres implementation writes into the `users` table already created by the scaffolding feature's `000001_init_schema` migration — no new migration needed
- The interface's real payoff is testing `auth.Service` against a fake repository, not database portability (this project stays on Postgres)
- Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`; DB setup, mux construction, and route/handler wiring all moved into `internal/app.go` and `internal/httpserver/route.go`. See the Plan's "Wiring — revised during implementation" section for the current shape and an open bug it introduced.

## Plan

### New files

```text
backend/
  internal/
    app.go                    # Run(cfg *config.Config): DB setup, mux, routes, serve — the process entrypoint
    auth/
      user.go              # User domain struct, ErrEmailTaken sentinel
      repository.go         # Repository interface
      service.go             # Service (validation, bcrypt, orchestration)
      service_test.go
      handler.go               # Handler (HTTP decode/encode only)
      handler_test.go
    postgres/
      auth_repository.go     # AuthRepository: Repository impl backed by pgx
    httpx/
      json.go                  # WriteJSON, WriteError shared helpers
    httpserver/
      server.go                 # NewServer() *http.ServeMux — builds the empty mux only
      route.go                   # RegisterRoutes(mux, cfg, pool): wires auth repo/service/handler AND registers routes
```

### `internal/auth/user.go`

```go
type User struct {
    ID          string
    Email       string
    DisplayName string
    CreatedAt   time.Time
}

var ErrEmailTaken = errors.New("auth: email already taken")
```

### `internal/auth/repository.go`

```go
type Repository interface {
    CreateUser(ctx context.Context, email, displayName, passwordHash string) (User, error)
}
```

Minimal on purpose — only the method this feature needs. `auth/login` extends it with `GetUserByEmail` when that feature is built, rather than pre-declaring methods nothing calls yet.

### `internal/auth/service.go`

```go
type Service struct {
    repo Repository
}

func NewService(repo Repository) *Service {
    return &Service{repo: repo}
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (User, map[string]string, error) {
    if errs := validateRegister(email, password, displayName); len(errs) > 0 {
        return User{}, errs, nil
    }

    hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
    if err != nil {
        return User{}, nil, fmt.Errorf("auth: hash password: %w", err)
    }

    user, err := s.repo.CreateUser(ctx, strings.ToLower(email), displayName, string(hash))
    if err != nil {
        return User{}, nil, err // ErrEmailTaken passes through as-is; handler checks errors.Is
    }
    return user, nil, nil
}

func validateRegister(email, password, displayName string) map[string]string {
    errs := map[string]string{}
    if _, err := mail.ParseAddress(email); err != nil {
        errs["email"] = "invalid format"
    }
    if len(password) < 8 {
        errs["password"] = "must be at least 8 characters"
    }
    if len(strings.TrimSpace(displayName)) == 0 || len(displayName) > 50 {
        errs["display_name"] = "must be 1-50 characters"
    }
    return errs
}
```

### `internal/auth/handler.go`

```go
type Handler struct {
    svc *Service
}

func NewHandler(svc *Service) *Handler {
    return &Handler{svc: svc}
}

type registerRequest struct {
    Email       string `json:"email"`
    Password    string `json:"password"`
    DisplayName string `json:"display_name"`
}

type registerResponse struct {
    ID          string `json:"id"`
    Email       string `json:"email"`
    DisplayName string `json:"display_name"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
    var req registerRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        httpx.WriteError(w, http.StatusBadRequest, "invalid_body")
        return
    }

    user, fieldErrs, err := h.svc.Register(r.Context(), req.Email, req.Password, req.DisplayName)
    if len(fieldErrs) > 0 {
        httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
        return
    }
    if errors.Is(err, ErrEmailTaken) {
        httpx.WriteError(w, http.StatusConflict, "email_taken")
        return
    }
    if err != nil {
        httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
        return
    }

    httpx.WriteJSON(w, http.StatusCreated, registerResponse{ID: user.ID, Email: user.Email, DisplayName: user.DisplayName})
}
```

### `internal/postgres/auth_repository.go`

```go
type AuthRepository struct {
    pool *pgxpool.Pool
}

func NewAuthRepository(pool *pgxpool.Pool) *AuthRepository {
    return &AuthRepository{pool: pool}
}

func (r *AuthRepository) CreateUser(ctx context.Context, email, displayName, passwordHash string) (auth.User, error) {
    var u auth.User
    err := r.pool.QueryRow(ctx, `
        INSERT INTO users (email, display_name, password_hash)
        VALUES ($1, $2, $3)
        RETURNING id, email, display_name, created_at
    `, email, displayName, passwordHash).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)

    if err != nil {
        var pgErr *pgconn.PgError
        if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
            return auth.User{}, auth.ErrEmailTaken
        }
        return auth.User{}, fmt.Errorf("postgres: create user: %w", err)
    }
    return u, nil
}
```

This is the only place in the codebase that imports both `pgx` and knows the unique-violation code — the interface above it never sees a `pgconn.PgError`.

### Wiring — revised during implementation

The original plan below put composition in `cmd/server/main.go` with a `Handlers` struct. That was superseded once we reorganized so `main.go` only loads config and calls `app.Run(cfg)`; DB setup, mux construction, and route registration all moved into `internal/app.go` and `internal/httpserver`. Current shape:

### `internal/httpserver/server.go`

```go
func NewServer() *http.ServeMux {
    server := http.NewServeMux()
    return server
}
```

Just builds the empty mux — no dependencies, nothing to wire.

### `internal/httpserver/route.go`

```go
func RegisterRoutes(server *http.ServeMux, cfg config.Config, pool *pgxpool.Pool) {
    healthzHandler := NewHealthzHandler(pool)
    server.HandleFunc("GET /healthz", healthzHandler)

    authRepo := postgres.NewAuthRepository(pool)
    authSvc := auth.NewService(authRepo)
    authHandler := auth.NewHandler(authSvc)

    server.HandleFunc("POST /auth/register", authHandler.Register)
}
```

Builds each domain's `repository → service → handler` chain and registers its route(s) in the same place, rather than in `main.go`. `races/create-race` (next-but-one) adds its own repo/service/handler construction and `server.HandleFunc` call here. `server` is a `*http.ServeMux`, mutated in place, so `RegisterRoutes` has no return value — callers keep using the mux they already passed in. (The healthz handler constructor was renamed from `healthzHandler` to `NewHealthzHandler` while this feature was in progress, to match the `New<StructName>`-style convention.)

### `internal/app.go`

```go
package internal

func Run(cfg *config.Config) {
    ctx := context.Background()

    pool, err := db.NewPool(ctx, cfg.DatabaseURL)
    if err != nil {
        log.Fatalf("db: %v", err)
    }
    defer pool.Close()

    if err := db.Migrate(cfg.DatabaseURL); err != nil {
        log.Fatalf("migrate: %v", err)
    }

    server := httpserver.NewServer()
    httpserver.RegisterRoutes(server, *cfg, pool)

    log.Printf("listening on :%s", cfg.Port)
    log.Fatal(http.ListenAndServe(":"+cfg.Port, server))
}
```

`cmd/server/main.go` is now just:

```go
package main

import (
    app "github.com/akkien/aviron/internal"
    "github.com/akkien/aviron/internal/config"
)

func main() {
    cfg := config.Load()
    app.Run(cfg)
}
```

**Resolved:** the `/feature review` (2026-07-18) flagged that `config.Load()` returning `config.Config` by value couldn't be passed to `Run(cfg *config.Config)`. Fixed by changing `config.Load()` to return `*Config` instead (`internal/config/config.go`), so `main.go`'s `cfg := config.Load()` is already a `*config.Config` and `app.Run(cfg)` type-checks. Verified with `go build`, `go vet`, `gofmt -l .`, and `go test ./... -race` (all clean), plus a manual `curl` against `/healthz` and `/auth/register` running the real reorganized wiring end to end.

**Still open, not blocking:** `package internal` living directly in `backend/internal/app.go` still requires the `app "github.com/akkien/aviron/internal"` import alias in `main.go` to read naturally — every other package here lives in its own subdirectory. Moving it to `internal/app/app.go` (`package app`) would drop the alias. Left as-is since it's cosmetic, not a correctness issue; worth doing whenever `internal/app.go` is next touched.

### Tests

- `service_test.go`: exercises `Service.Register` against a hand-written fake `Repository` (in-memory map) — no Postgres needed for validation/hashing/orchestration logic
- `handler_test.go`: exercises `Handler.Register` the same way, via `httptest`, against the same fake — confirms status codes and response bodies without touching the DB
- No new test needs a real Postgres connection for this feature (a change from `healthz_test.go`'s approach) — the repository interface is precisely what makes that possible

### New dependency

- `golang.org/x/crypto/bcrypt` — `go get golang.org/x/crypto`

## Notes

- No email verification flow — out of scope per context/project-overview.md §1
- No rate limiting on this endpoint yet — acceptable for Phase 1, revisit if abused
- Unlike the scaffolding feature's `healthz_test.go` (which needs real Postgres, with a `t.Skip` fallback), this feature's tests run against a fake `Repository` and need no database at all — the layering introduced here is what makes that possible
- Next feature per context/features/phase-1-plan.md: `auth/login`

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
