# Current Feature: Join Race

## Status

In Progress

## Goals

- `POST /races/{id}/join` (authenticated) → `200` with `{race_id, session_token}`
- `400 invalid_race_id` if `{id}` isn't a well-formed UUID
- `404 race_not_found` if the race doesn't exist
- `409 race_not_pending` if the race isn't `pending`; `409 already_joined` if the caller already joined
- `session_token` is a signed HS256 JWT (`race_id`, `user_id` claims, 6h TTL, same `JWT_SECRET`) — no DB column needed
- `go test ./... -race` passes for the extended `internal/race` package

## Explain

- Extends the existing `internal/race` package (`RaceRepository`/`RaceService`/`RaceHandler`) rather than creating a new one — same pattern as `auth/login` extending `auth`'s types instead of a separate package
- `RaceService` gains a `jwtSecret []byte` field via `NewRaceService(repo, jwtSecret)`, mirroring `AuthService`'s exact shape — each service that signs tokens holds its own copy of the secret rather than sharing a JWT-signing helper package; not worth abstracting for two call sites
- Three new domain sentinel errors: `ErrRaceNotFound` (translated from `pgx.ErrNoRows` in `GetRace`, same convention as `auth.ErrUserNotFound`), `ErrAlreadyJoined` (translated from a `race_participants` unique-violation, same convention as `auth.ErrEmailTaken`), and `ErrRaceNotPending` (a service-level business-rule check, not a repository translation — nothing in Postgres itself rejects this)
- `{id}` UUID-format validation happens in the **handler**, not the service — it's a malformed-request concern (plain `{"error": code}`) like `Register`'s JSON-decode failure, not a field-keyed validation error like `name`/`distance_meters`. Validated with a small regex, not a `github.com/google/uuid` dependency — consistent with this project's existing choice to keep ids as plain `string` everywhere (noted in the `jwt-middleware` feature)
- No new migration: `race_participants` already has every column this feature needs (`race_id`, `user_id`, plus defaults for the rest) from the scaffolding migration

## Plan

### New/changed files

```text
backend/
  internal/
    race/
      race.go              # + ErrRaceNotFound, ErrAlreadyJoined, ErrRaceNotPending
      repository.go          # + GetRace, AddParticipant in the RaceRepository interface
      service.go               # + jwtSecret field, NewRaceService gains a param, + JoinRace method
      service_test.go           # existing NewRaceService(repo) calls updated; + JoinRace tests
      handler.go                 # + Join handler, isValidUUID helper
      handler_test.go             # existing newTestHandler() updated; + Join tests
      helpers_test.go               # fakeRepository gains GetRace, AddParticipant, participants map
      dtos.go                        # + joinRaceResponse
    postgres/
      race_repository.go              # + GetRace, AddParticipant (reuses the existing uniqueViolation const from auth_repository.go)
    httpserver/
      route.go                          # + POST /races/{id}/join route, NewRaceService call gains cfg.JWTSecret
```

### `internal/race/race.go`

```go
var ErrRaceNotFound = errors.New("race: not found")
var ErrAlreadyJoined = errors.New("race: already joined")
var ErrRaceNotPending = errors.New("race: not pending")
```

### `internal/race/repository.go`

```go
type RaceRepository interface {
    CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (Race, error)
    GetRace(ctx context.Context, raceID string) (Race, error)
    AddParticipant(ctx context.Context, raceID, userID string) error
}
```

### `internal/race/service.go`

```go
type RaceService struct {
    repo      RaceRepository
    jwtSecret []byte
}

func NewRaceService(repo RaceRepository, jwtSecret []byte) *RaceService {
    return &RaceService{repo: repo, jwtSecret: jwtSecret}
}

func (s *RaceService) JoinRace(ctx context.Context, raceID, userID string) (sessionToken string, err error) {
    r, err := s.repo.GetRace(ctx, raceID)
    if err != nil {
        return "", err // ErrRaceNotFound passes through as-is; handler checks errors.Is
    }

    if r.Status != "pending" {
        return "", ErrRaceNotPending
    }

    if err := s.repo.AddParticipant(ctx, raceID, userID); err != nil {
        return "", err // ErrAlreadyJoined passes through as-is
    }

    claims := jwt.MapClaims{
        "race_id": raceID,
        "user_id": userID,
        "exp":     time.Now().Add(6 * time.Hour).Unix(),
    }
    signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
    if err != nil {
        return "", fmt.Errorf("race: sign session token: %w", err)
    }

    return signed, nil
}
```

`CreateRace` is unchanged except that it's now a method on a `RaceService` that also holds `jwtSecret` — same shape as `Register` unaffected by `Login` adding that field to `AuthService`. No `fieldErrs` return here (unlike `CreateRace`) since the only input validation — UUID format — already happened in the handler before this is called.

### `internal/race/dtos.go`

```go
type joinRaceResponse struct {
    RaceID       string `json:"race_id"`
    SessionToken string `json:"session_token"`
}
```

### `internal/race/handler.go`

```go
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(s string) bool {
    return uuidPattern.MatchString(s)
}

// Join godoc
// @Summary Join a race
// @Description Joins an existing race as a participant, returning a per-race session token
// @Tags races
// @Produce json
// @Param id path string true "Race ID"
// @Success 200 {object} joinRaceResponse
// @Failure 400 {object} map[string]string "error: invalid_race_id"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Failure 404 {object} map[string]string "error: race_not_found"
// @Failure 409 {object} map[string]string "error: already_joined | race_not_pending"
// @Router /races/{id}/join [post]
func (h *RaceHandler) Join(w http.ResponseWriter, r *http.Request) {
    userID, ok := middleware.UserIDFromContext(r.Context())
    if !ok {
        httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
        return
    }

    raceID := r.PathValue("id")
    if !isValidUUID(raceID) {
        httpx.WriteError(w, http.StatusBadRequest, "invalid_race_id")
        return
    }

    sessionToken, err := h.svc.JoinRace(r.Context(), raceID, userID)
    switch {
    case errors.Is(err, ErrRaceNotFound):
        httpx.WriteError(w, http.StatusNotFound, "race_not_found")
        return
    case errors.Is(err, ErrRaceNotPending):
        httpx.WriteError(w, http.StatusConflict, "race_not_pending")
        return
    case errors.Is(err, ErrAlreadyJoined):
        httpx.WriteError(w, http.StatusConflict, "already_joined")
        return
    case err != nil:
        httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
        return
    }

    httpx.WriteJSON(w, http.StatusOK, joinRaceResponse{
        RaceID:       raceID,
        SessionToken: sessionToken,
    })
}
```

### `internal/postgres/race_repository.go`

```go
func (r *RaceRepository) GetRace(ctx context.Context, raceID string) (race.Race, error) {
    var rc race.Race

    err := r.pool.QueryRow(ctx, `
        SELECT id, name, distance_meters, status, created_by, created_at
        FROM races
        WHERE id = $1
    `, raceID).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt)

    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return race.Race{}, race.ErrRaceNotFound
        }
        return race.Race{}, fmt.Errorf("postgres: get race: %w", err)
    }
    return rc, nil
}

func (r *RaceRepository) AddParticipant(ctx context.Context, raceID, userID string) error {
    _, err := r.pool.Exec(ctx, `
        INSERT INTO race_participants (race_id, user_id)
        VALUES ($1, $2)
    `, raceID, userID)

    if err != nil {
        var pgErr *pgconn.PgError
        if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
            return race.ErrAlreadyJoined
        }
        return fmt.Errorf("postgres: add participant: %w", err)
    }
    return nil
}
```

`uniqueViolation` is the existing package-level const already defined in `auth_repository.go` (same `package postgres`) — reused as-is, not redefined.

### `internal/httpserver/route.go`

```go
raceSvc := race.NewRaceService(raceRepo, []byte(cfg.JWTSecret))
...
server.Handle("POST /races", requireAuth(http.HandlerFunc(raceHandler.Create)))
server.Handle("POST /races/{id}/join", requireAuth(http.HandlerFunc(raceHandler.Join)))
```

### Tests

- `helpers_test.go`: `fakeRepository` gains a `participants map[string]bool` field, `GetRace` (linear scan, returns `ErrRaceNotFound`), and `AddParticipant` (keyed by `raceID+":"+userID`, returns `ErrAlreadyJoined` on repeat)
- `service_test.go`: update every `race.NewRaceService(repo)` call to `race.NewRaceService(repo, []byte("test-secret"))`; add `TestRaceService_JoinRace_Success`, `TestRaceService_JoinRace_RaceNotFound`, `TestRaceService_JoinRace_AlreadyJoined`, `TestRaceService_JoinRace_RaceNotPending` (mutates the fake's race status directly — same-package test files can reach unexported fields)
- `handler_test.go`: same constructor update; add `TestRaceHandler_Join_OK`, `TestRaceHandler_Join_InvalidRaceID`, `TestRaceHandler_Join_NotFound`, `TestRaceHandler_Join_AlreadyJoined`, `TestRaceHandler_Join_NotPending`, `TestRaceHandler_Join_MissingAuth`

### New dependency

- None

No divergence from context/project-overview.md.

## Notes

- After this merges, regenerate Swagger docs (`make docs`)
- Next feature per context/features/phase-1-plan.md: `races/start-race`

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
- **User Registration** (2026-07-18) — `POST /auth/register` (bcrypt cost 12, `409 email_taken`, `400` field-keyed validation errors), introducing the layered `handler → service → repository` architecture per domain (documented in context/coding-standards.md, "Backend Architecture") plus `internal/postgres` for driver-specific repos and `internal/httpx` for shared response helpers. Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`, with DB setup and route registration moved into `internal/app.go` and `internal/httpserver/route.go`. Also planned the rest of Phase 1 into small specs under context/features/ (auth, races, frontend-client) with an index at context/features/phase-1-plan.md. Known cosmetic gap, not blocking: `internal/app.go` declares `package internal` directly instead of living in its own `internal/app/` subdirectory, so `main.go` needs an import alias (`app "github.com/akkien/aviron/internal"`) — worth tidying next time that file is touched. Next feature per context/features/phase-1-plan.md: `auth/login`.
- **Swagger API Docs** (2026-07-18) — Documented `GET /healthz` and `POST /auth/register` with `github.com/swaggo/http-swagger` annotations, generated `backend/docs/` via `swag init`, and served the UI at `GET /swagger/`. Added a `make docs` target to regenerate after future annotation changes. Not part of the Phase 1 feature plan — a standalone infra addition, branched and merged the same way.
- **Login & JWT Issuance** (2026-07-18) — `POST /auth/login` (bcrypt verify, HS256 JWT with `sub`/`email`/`exp` claims, 24h expiry), extending `auth.AuthRepository` with `GetUserByEmail` and collapsing "user not found"/"wrong password" into one `ErrInvalidCredentials` so the response can't be used to enumerate accounts. Renamed the domain's types to match `internal/postgres`'s existing convention (`Handler`/`Service`/`Repository` → `AuthHandler`/`AuthService`/`AuthRepository`, constructors `NewAuthHandler`/`NewAuthService`), moved all request/response DTOs into a new `dtos.go`, and documented both as a standing convention in coding-standards.md for future domain packages (e.g. `race` → `RaceHandler`/`RaceService`/`RaceRepository`). Also fixed a latent `.PHONY` bug in the Makefile that made `make docs` a no-op. **Verification gap carried into master:** `go build`/`go vet`/`gofmt -l .` are clean, but `go test ./... -race` was not run after the rename (skipped per explicit request) — the rename was mechanical with no logic changes, so risk is low, but this is not independently confirmed; run the full suite before building on top of this. Next feature per context/features/phase-1-plan.md: `auth/jwt-middleware`.
- **JWT Auth Middleware** (2026-07-18) — `internal/middleware.Auth(jwtSecret)`, verifying the JWT `Login` issues (HS256, `sub`/`exp` claims, explicit expiry check alongside the library's implicit one) and exposing the caller's user id via `UserIDFromContext`. Moved mid-feature from `internal/auth/middleware.go` (`auth.RequireAuth`) into its own top-level `internal/middleware` package (`middleware.Auth`), since the logic has zero dependency on `internal/auth`'s domain types — documented as a standing convention in coding-standards.md ("cross-cutting middleware gets its own top-level package"). Verified: `go build`/`go vet`/`gofmt -l .` clean, and all `TestAuth_*` tests pass (closing the verification gap the previous feature left open, at least for this package). Not wired into any route yet — nothing to protect until `races/create-race`. Also backfilled concise `docs/feature-log.md` entries for User Registration and Login & JWT Issuance, which hadn't been documented there yet. Next feature per context/features/phase-1-plan.md: `races/create-race`.
- **Create Race** (2026-07-18) — `POST /races` (authenticated), the first domain package beyond `auth` (`internal/race`: `RaceHandler`/`RaceService`/`RaceRepository`, `NewRaceHandler`/`NewRaceService`, `dtos.go`), following the layered convention exactly — deliberately diverging from the feature spec's older flat-function handler wording, which predates that convention. No new migration needed: `races` already had every column this feature uses from the scaffolding migration. This is the first route actually wrapped with `middleware.Auth`, closing the loop the JWT Auth Middleware feature left open. Verified: `go build`/`go vet`/`gofmt -l .` clean, and the *entire* test suite passed (`go test ./... -race`), including `internal/auth` — this also closes the verification gap the Login & JWT Issuance feature had left open. Manual curl/DB verification deferred until Phase 1 is fully done, per explicit request. Next feature per context/features/phase-1-plan.md: `races/join-race`.
