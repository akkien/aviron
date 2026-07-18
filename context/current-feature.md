# Current Feature: Create Race

## Status

In Progress

## Goals

- `POST /races` (authenticated) → `201` with `{id, name, distance_meters, status: "pending", created_by, created_at}`
- `400` field-keyed errors for invalid `name` (empty/>100 chars) or `distance_meters` (not a positive integer)
- The creator is recorded in `created_by` but not auto-joined as a participant
- `go test ./... -race` passes for the new `internal/race` package
- This is the first endpoint actually wrapped with `middleware.Auth` — `internal/middleware` finally gets wired into a route

## Explain

- First domain package beyond `auth` — follows the exact same layered shape (`RaceHandler`/`RaceService`/`RaceRepository`, `NewRaceHandler`/`NewRaceService`, `dtos.go`) documented in coding-standards.md's "Backend Architecture" and "Personal naming styles" sections
- The spec (`context/features/races/create-race.md`) describes an older flat-function handler style (`CreateRaceHandler(pool) http.HandlerFunc`) written before that domain-prefixed convention was established during the auth features. Following the current standard instead of the spec's literal wording — noting the divergence here rather than silently ignoring it.
- No new migration: `races` already has every column this feature needs (`id`, `name`, `distance_meters`, `status`, `created_by`, `created_at`) from the scaffolding feature's `000001_init_schema` migration. `prompt_text` (needed by `races/start-race`, next-but-one) isn't touched here.
- `distance_meters` is the typing race's target word count, not a literal distance (context/project-overview.md §13) — the domain struct and DB column names are unchanged, only their meaning
- The handler reads the caller's id via `middleware.UserIDFromContext` for `created_by` — this is the first time anything in the codebase actually calls `middleware.Auth`/`UserIDFromContext` in a real request path, closing the loop the `jwt-middleware` feature left open

## Plan

### New files

```text
backend/
  internal/
    race/
      race.go              # Race domain struct
      repository.go          # RaceRepository interface
      service.go               # RaceService, NewRaceService, CreateRace, validation
      service_test.go
      handler.go                 # RaceHandler, NewRaceHandler, Create
      handler_test.go
      helpers_test.go               # fakeRepository
      dtos.go                        # createRaceRequest, createRaceResponse
    postgres/
      race_repository.go              # RaceRepository impl backed by pgx
  internal/httpserver/route.go          # wires RaceRepository/RaceService/RaceHandler, registers POST /races behind middleware.Auth
```

### `internal/race/race.go`

```go
package race

import "time"

type Race struct {
    ID             string
    Name           string
    DistanceMeters int
    Status         string
    CreatedBy      string
    CreatedAt      time.Time
}
```

Minimal on purpose, same pattern as `auth.User` — only the fields this feature actually returns. `StartedAt`/`EndedAt`/`PromptText` get added when `races/start-race` needs them, not preemptively.

### `internal/race/repository.go`

```go
package race

import "context"

type RaceRepository interface {
    CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (Race, error)
}
```

### `internal/race/service.go`

```go
package race

import (
    "context"
    "strings"
)

type RaceService struct {
    repo RaceRepository
}

func NewRaceService(repo RaceRepository) *RaceService {
    return &RaceService{repo: repo}
}

func (s *RaceService) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (r Race, fieldErrs map[string]string, err error) {
    if errs := validateCreateRace(name, distanceMeters); len(errs) > 0 {
        return Race{}, errs, nil
    }

    r, err = s.repo.CreateRace(ctx, strings.TrimSpace(name), distanceMeters, createdBy)
    if err != nil {
        return Race{}, nil, err
    }
    return r, nil, nil
}

func validateCreateRace(name string, distanceMeters int) map[string]string {
    errs := map[string]string{}

    trimmed := strings.TrimSpace(name)
    if len(trimmed) == 0 || len(name) > 100 {
        errs["name"] = "must be 1-100 characters"
    }
    if distanceMeters <= 0 {
        errs["distance_meters"] = "must be a positive integer"
    }

    return errs
}
```

Mirrors `auth.validateRegister`'s exact quirk (checks the *trimmed* string for emptiness but the *original* string's length against the max) for consistency with existing code, not because it's necessarily the ideal validation.

### `internal/race/dtos.go`

```go
package race

type createRaceRequest struct {
    Name           string `json:"name"`
    DistanceMeters int    `json:"distance_meters"`
}

type createRaceResponse struct {
    ID             string `json:"id"`
    Name           string `json:"name"`
    DistanceMeters int    `json:"distance_meters"`
    Status         string `json:"status"`
    CreatedBy      string `json:"created_by"`
    CreatedAt      string `json:"created_at"`
}
```

### `internal/race/handler.go`

```go
package race

type RaceHandler struct {
    svc *RaceService
}

func NewRaceHandler(svc *RaceService) *RaceHandler {
    return &RaceHandler{svc: svc}
}

// Create godoc
// @Summary Create a race
// @Description Creates a new typing race with a name and target word count
// @Tags races
// @Accept json
// @Produce json
// @Param request body createRaceRequest true "Create race payload"
// @Success 201 {object} createRaceResponse
// @Failure 400 {object} map[string]interface{} "field-keyed validation errors"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Router /races [post]
func (h *RaceHandler) Create(w http.ResponseWriter, r *http.Request) {
    userID, ok := middleware.UserIDFromContext(r.Context())
    if !ok {
        httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
        return
    }

    var req createRaceRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        httpx.WriteError(w, http.StatusBadRequest, "invalid_body")
        return
    }

    created, fieldErrs, err := h.svc.CreateRace(r.Context(), req.Name, req.DistanceMeters, userID)
    if len(fieldErrs) > 0 {
        httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
        return
    }
    if err != nil {
        httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
        return
    }

    httpx.WriteJSON(w, http.StatusCreated, createRaceResponse{
        ID:             created.ID,
        Name:           created.Name,
        DistanceMeters: created.DistanceMeters,
        Status:         created.Status,
        CreatedBy:      created.CreatedBy,
        CreatedAt:      created.CreatedAt.Format(time.RFC3339),
    })
}
```

The `UserIDFromContext` check runs even though `middleware.Auth` will always be wrapped around this handler in `route.go` — defense in depth, and it's what makes `TestRaceHandler_Create_MissingAuth` (calling `h.Create` directly, no middleware) meaningful.

### `internal/postgres/race_repository.go`

```go
package postgres

import (
    "context"
    "fmt"

    "github.com/akkien/aviron/internal/race"
    "github.com/jackc/pgx/v5/pgxpool"
)

type RaceRepository struct {
    pool *pgxpool.Pool
}

func NewRaceRepository(pool *pgxpool.Pool) *RaceRepository {
    return &RaceRepository{pool: pool}
}

func (r *RaceRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
    var rc race.Race

    err := r.pool.QueryRow(ctx, `
        INSERT INTO races (name, distance_meters, created_by)
        VALUES ($1, $2, $3)
        RETURNING id, name, distance_meters, status, created_by, created_at
    `, name, distanceMeters, createdBy).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt)

    if err != nil {
        return race.Race{}, fmt.Errorf("postgres: create race: %w", err)
    }

    return rc, nil
}
```

No error translation needed here (unlike `AuthRepository.CreateUser`) — there's no unique constraint on race name to catch.

### `internal/httpserver/route.go`

```go
requireAuth := middleware.Auth([]byte(cfg.JWTSecret))

raceRepo := postgres.NewRaceRepository(pool)
raceSvc := race.NewRaceService(raceRepo)
raceHandler := race.NewRaceHandler(raceSvc)

server.Handle("POST /races", requireAuth(http.HandlerFunc(raceHandler.Create)))
```

`server.Handle` (not `HandleFunc`) since wrapping with `middleware.Auth` produces an `http.Handler`, not a bare func. This is the first route in the codebase actually wrapped with auth middleware.

### Tests

- `helpers_test.go`: `fakeRepository` — in-memory `RaceRepository`, no DB
- `service_test.go`: `TestRaceService_CreateRace_Success` (trims name, status defaults to `pending`, `created_by` set); `TestRaceService_CreateRace_ValidationErrors` (table-driven: empty name, whitespace-only name, zero/negative distance)
- `handler_test.go`: mints a real signed JWT with `jwt.NewWithClaims` (same approach `internal/middleware`'s own tests use — `internal/race` doesn't depend on `internal/auth`'s `Login` flow to get a token) and drives requests through `middleware.Auth(secret)(http.HandlerFunc(h.Create))` for the realistic cases (`TestRaceHandler_Create_Created`, `_ValidationError`, `_InvalidBody`), plus `TestRaceHandler_Create_MissingAuth` calling `h.Create` directly with no middleware wrapping, to exercise the handler's own defensive check

### New dependency

- None — reuses `github.com/golang-jwt/jwt/v5` (test-only, for minting a token) and existing `pgx`/`httpx` patterns

No divergence from context/project-overview.md; the only divergences are from the feature spec's literal handler-style wording (noted in Explain above).

## Notes

- After this merges, regenerate Swagger docs (`make docs`) to pick up the new `Create` annotations
- Next feature per context/features/phase-1-plan.md: `races/join-race`

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
- **User Registration** (2026-07-18) — `POST /auth/register` (bcrypt cost 12, `409 email_taken`, `400` field-keyed validation errors), introducing the layered `handler → service → repository` architecture per domain (documented in context/coding-standards.md, "Backend Architecture") plus `internal/postgres` for driver-specific repos and `internal/httpx` for shared response helpers. Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`, with DB setup and route registration moved into `internal/app.go` and `internal/httpserver/route.go`. Also planned the rest of Phase 1 into small specs under context/features/ (auth, races, frontend-client) with an index at context/features/phase-1-plan.md. Known cosmetic gap, not blocking: `internal/app.go` declares `package internal` directly instead of living in its own `internal/app/` subdirectory, so `main.go` needs an import alias (`app "github.com/akkien/aviron/internal"`) — worth tidying next time that file is touched. Next feature per context/features/phase-1-plan.md: `auth/login`.
- **Swagger API Docs** (2026-07-18) — Documented `GET /healthz` and `POST /auth/register` with `github.com/swaggo/http-swagger` annotations, generated `backend/docs/` via `swag init`, and served the UI at `GET /swagger/`. Added a `make docs` target to regenerate after future annotation changes. Not part of the Phase 1 feature plan — a standalone infra addition, branched and merged the same way.
- **Login & JWT Issuance** (2026-07-18) — `POST /auth/login` (bcrypt verify, HS256 JWT with `sub`/`email`/`exp` claims, 24h expiry), extending `auth.AuthRepository` with `GetUserByEmail` and collapsing "user not found"/"wrong password" into one `ErrInvalidCredentials` so the response can't be used to enumerate accounts. Renamed the domain's types to match `internal/postgres`'s existing convention (`Handler`/`Service`/`Repository` → `AuthHandler`/`AuthService`/`AuthRepository`, constructors `NewAuthHandler`/`NewAuthService`), moved all request/response DTOs into a new `dtos.go`, and documented both as a standing convention in coding-standards.md for future domain packages (e.g. `race` → `RaceHandler`/`RaceService`/`RaceRepository`). Also fixed a latent `.PHONY` bug in the Makefile that made `make docs` a no-op. **Verification gap carried into master:** `go build`/`go vet`/`gofmt -l .` are clean, but `go test ./... -race` was not run after the rename (skipped per explicit request) — the rename was mechanical with no logic changes, so risk is low, but this is not independently confirmed; run the full suite before building on top of this. Next feature per context/features/phase-1-plan.md: `auth/jwt-middleware`.
- **JWT Auth Middleware** (2026-07-18) — `internal/middleware.Auth(jwtSecret)`, verifying the JWT `Login` issues (HS256, `sub`/`exp` claims, explicit expiry check alongside the library's implicit one) and exposing the caller's user id via `UserIDFromContext`. Moved mid-feature from `internal/auth/middleware.go` (`auth.RequireAuth`) into its own top-level `internal/middleware` package (`middleware.Auth`), since the logic has zero dependency on `internal/auth`'s domain types — documented as a standing convention in coding-standards.md ("cross-cutting middleware gets its own top-level package"). Verified: `go build`/`go vet`/`gofmt -l .` clean, and all `TestAuth_*` tests pass (closing the verification gap the previous feature left open, at least for this package). Not wired into any route yet — nothing to protect until `races/create-race`. Also backfilled concise `docs/feature-log.md` entries for User Registration and Login & JWT Issuance, which hadn't been documented there yet. Next feature per context/features/phase-1-plan.md: `races/create-race`.
