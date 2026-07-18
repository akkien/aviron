# Current Feature: Login & JWT Issuance

## Status

In Progress

## Goals

- `POST /auth/login` returns `200` with `{"token": string, "expires_at": string (RFC3339)}` for a correct email/password
- `401 {"error":"invalid_credentials"}` for a wrong password **or** an unknown email — identical response either way, so the client can't tell which was wrong
- JWT signed HS256, claims `sub` (user id), `email`, `exp` (24h from issuance), secret from `JWT_SECRET` env var
- `go test ./... -race` passes, including new tests for `Service.Login` and `Handler.Login` against a fake repository (no real Postgres needed)

## Explain

- Second half of the auth flow started by User Registration; defines the JWT format `auth/jwt-middleware` (next feature) will verify
- Extends `auth.AuthRepository` with `GetUserByEmail`; the Postgres implementation translates `pgx.ErrNoRows` into a new `auth.ErrUserNotFound` sentinel — the same repo-boundary error-translation convention already used for `ErrEmailTaken`
- `auth.AuthService` gains a `jwtSecret []byte` field via a new `NewAuthService(repo, jwtSecret)` constructor param, since the signing secret is service-level config, not per-call data. This changes the existing constructor signature, so `internal/auth`'s existing tests and `internal/httpserver/route.go`'s wiring both need a one-line update to pass a secret.
- Both "user not found" and "wrong password" collapse to the same `auth.ErrInvalidCredentials` in the service layer, so the handler always responds `401` identically — this is what prevents leaking which half of the credentials was wrong
- No JWT verification in this feature — parsing/validating incoming tokens is `auth/jwt-middleware`'s job. This feature only signs and issues them.
- **Renamed mid-feature** (per explicit request, now documented in context/coding-standards.md): `Handler`→`AuthHandler`, `Service`→`AuthService`, `Repository`→`AuthRepository` (interface), with constructors `NewAuthHandler`/`NewAuthService` — domain types are now always prefixed with the domain name, matching `postgres.AuthRepository`'s existing convention. Request/response DTOs (`registerRequest`, `registerResponse`, `loginRequest`, `loginResponse`) were also consolidated out of `handler.go` into a new `internal/auth/dtos.go`, so every future domain does the same.

## Plan

### New/changed files

```text
backend/
  internal/
    config/
      config.go              # + JWTSecret field
    auth/
      user.go               # + PasswordHash field on User, + ErrUserNotFound, + ErrInvalidCredentials
      repository.go           # AuthRepository interface (renamed from Repository), + GetUserByEmail
      service.go               # AuthService (renamed from Service), + jwtSecret field, NewAuthService gains a param, + Login method
      service_test.go           # NewAuthService(repo, secret) calls; + Login tests
      handler.go                 # AuthHandler (renamed from Handler), + Login handler, swaggo annotations
      handler_test.go             # NewAuthHandler/NewAuthService calls; + Login tests
      helpers_test.go               # fakeRepository gains GetUserByEmail + stores PasswordHash
      dtos.go                        # new: registerRequest/Response, loginRequest/Response — moved out of handler.go
    postgres/
      auth_repository.go            # + GetUserByEmail
    httpserver/
      route.go                       # + POST /auth/login route, NewAuthService call gains cfg.JWTSecret
```

### `internal/config/config.go`

```go
type Config struct {
    DatabaseURL string
    Port        string
    JWTSecret   string
}

func Load() *Config {
    _ = godotenv.Load()

    return &Config{
        DatabaseURL: getEnv("DATABASE_URL", "postgres://aviron:aviron@localhost:5432/aviron?sslmode=disable"),
        Port:        getEnv("PORT", "8080"),
        JWTSecret:   getEnv("JWT_SECRET", "dev-only-secret-change-me"),
    }
}
```

### `internal/auth/user.go`

```go
type User struct {
    ID           string
    Email        string
    DisplayName  string
    PasswordHash string
    CreatedAt    time.Time
}

var ErrEmailTaken         = errors.New("auth: email already taken")
var ErrUserNotFound       = errors.New("auth: user not found")
var ErrInvalidCredentials = errors.New("auth: invalid credentials")
```

`PasswordHash` is new on the domain struct — `CreateUser`'s `RETURNING` clause still omits it (Register never needs it back), but `GetUserByEmail` populates it so `Service.Login` can run `bcrypt.CompareHashAndPassword`. It never appears in any response DTO (`loginResponse`/`registerResponse` are separate structs that don't embed `User`).

### `internal/auth/repository.go`

```go
type AuthRepository interface {
    CreateUser(ctx context.Context, email, displayName, passwordHash string) (User, error)
    GetUserByEmail(ctx context.Context, email string) (User, error)
}
```

### `internal/auth/service.go`

```go
type AuthService struct {
    repo      AuthRepository
    jwtSecret []byte
}

func NewAuthService(repo AuthRepository, jwtSecret []byte) *AuthService {
    return &AuthService{repo: repo, jwtSecret: jwtSecret}
}

func (s *AuthService) Login(ctx context.Context, email, password string) (token string, expiresAt time.Time, err error) {
    if email == "" || password == "" {
        return "", time.Time{}, ErrInvalidCredentials
    }

    user, err := s.repo.GetUserByEmail(ctx, strings.ToLower(email))
    if err != nil {
        if errors.Is(err, ErrUserNotFound) {
            return "", time.Time{}, ErrInvalidCredentials
        }
        return "", time.Time{}, err
    }

    if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
        return "", time.Time{}, ErrInvalidCredentials
    }

    expiresAt = time.Now().Add(24 * time.Hour)
    claims := jwt.MapClaims{
        "sub":   user.ID,
        "email": user.Email,
        "exp":   expiresAt.Unix(),
    }
    signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
    if err != nil {
        return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
    }

    return signed, expiresAt, nil
}
```

`Register` is unchanged except that it's now a method on an `AuthService` that also holds `jwtSecret` — it just doesn't use that field.

### `internal/auth/dtos.go`

```go
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

type loginRequest struct {
    Email    string `json:"email"`
    Password string `json:"password"`
}

type loginResponse struct {
    Token     string `json:"token"`
    ExpiresAt string `json:"expires_at"`
}
```

All request/response DTOs for the `auth` domain live in this one file, not scattered across `handler.go`'s individual handler functions.

### `internal/auth/handler.go`

```go
type AuthHandler struct {
    svc *AuthService
}

func NewAuthHandler(svc *AuthService) *AuthHandler {
    return &AuthHandler{svc: svc}
}

// Login godoc
// @Summary Log in
// @Description Exchanges email and password for a JWT
// @Tags auth
// @Accept json
// @Produce json
// @Param request body loginRequest true "Login payload"
// @Success 200 {object} loginResponse
// @Failure 401 {object} map[string]string "error: invalid_credentials"
// @Router /auth/login [post]
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
    var req loginRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        httpx.WriteError(w, http.StatusBadRequest, "invalid_body")
        return
    }

    token, expiresAt, err := h.svc.Login(r.Context(), req.Email, req.Password)
    if errors.Is(err, ErrInvalidCredentials) {
        httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials")
        return
    }
    if err != nil {
        httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
        return
    }

    httpx.WriteJSON(w, http.StatusOK, loginResponse{
        Token:     token,
        ExpiresAt: expiresAt.Format(time.RFC3339),
    })
}
```

### `internal/postgres/auth_repository.go`

```go
func (r *AuthRepository) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
    var u auth.User

    err := r.pool.QueryRow(ctx, `
        SELECT id, email, display_name, password_hash, created_at
        FROM users
        WHERE email = $1
    `, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.CreatedAt)

    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return auth.User{}, auth.ErrUserNotFound
        }
        return auth.User{}, fmt.Errorf("postgres: get user by email: %w", err)
    }

    return u, nil
}
```

Needs a new import: `github.com/jackc/pgx/v5` (for `pgx.ErrNoRows` — distinct from the `pgconn` import already used for `ErrEmailTaken`'s unique-violation check).

### `internal/httpserver/route.go`

```go
authRepo := postgres.NewAuthRepository(pool)
authSvc := auth.NewAuthService(authRepo, []byte(cfg.JWTSecret))
authHandler := auth.NewAuthHandler(authSvc)

server.HandleFunc("POST /auth/register", authHandler.Register)
server.HandleFunc("POST /auth/login", authHandler.Login)
```

### New dependency

- `github.com/golang-jwt/jwt/v5` — `go get github.com/golang-jwt/jwt/v5`

### Tests

- `helpers_test.go`: `fakeRepository.CreateUser` now stores `PasswordHash` on the saved `auth.User` (not just the separate `lastPasswordHash` field already used by the Register test); add `GetUserByEmail` returning `auth.ErrUserNotFound` when the email isn't in the map
- `service_test.go`: update every existing `auth.NewService(repo)` call to `auth.NewAuthService(repo, []byte("test-secret"))`; add `TestService_Login_Success` (valid credentials → non-empty token, `expiresAt` ~24h out), `TestService_Login_WrongPassword`, `TestService_Login_UnknownEmail` (both → `ErrInvalidCredentials`), `TestService_Login_EmptyFields`
- `handler_test.go`: same constructor update; add `TestHandler_Login_OK`, `TestHandler_Login_Unauthorized` (wrong password and unknown email, table-driven), `TestHandler_Login_InvalidBody`

No divergence from context/project-overview.md — matches §4.3's "per-race session_token is distinct from the main JWT" note (that's a separate token, built in `races/join-race`, not this feature) and §11's suggested stack (no new infra, just a JWT library).

## Notes

- `JWT_SECRET` needs a real value in `backend/.env` and a placeholder in `backend/.env.example`, following the pattern set in the scaffolding feature
- No refresh tokens in Phase 1 — re-login once the JWT expires
- **Verification gap:** `go build`, `go vet`, and `gofmt -l .` are all clean, but `go test ./... -race` was not actually run after the `AuthHandler`/`AuthService`/`AuthRepository` rename (skipped per explicit request during this session). The rename was mechanical — type renames plus call-site updates, no logic changes — so risk is low, but goal #4 is not independently confirmed. Run `go test ./... -race` before relying on this being fully verified.
- Next feature per context/features/phase-1-plan.md: `auth/jwt-middleware`

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
- **User Registration** (2026-07-18) — `POST /auth/register` (bcrypt cost 12, `409 email_taken`, `400` field-keyed validation errors), introducing the layered `handler → service → repository` architecture per domain (documented in context/coding-standards.md, "Backend Architecture") plus `internal/postgres` for driver-specific repos and `internal/httpx` for shared response helpers. Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`, with DB setup and route registration moved into `internal/app.go` and `internal/httpserver/route.go`. Also planned the rest of Phase 1 into small specs under context/features/ (auth, races, frontend-client) with an index at context/features/phase-1-plan.md. Known cosmetic gap, not blocking: `internal/app.go` declares `package internal` directly instead of living in its own `internal/app/` subdirectory, so `main.go` needs an import alias (`app "github.com/akkien/aviron/internal"`) — worth tidying next time that file is touched. Next feature per context/features/phase-1-plan.md: `auth/login`.
- **Swagger API Docs** (2026-07-18) — Documented `GET /healthz` and `POST /auth/register` with `github.com/swaggo/http-swagger` annotations, generated `backend/docs/` via `swag init`, and served the UI at `GET /swagger/`. Added a `make docs` target to regenerate after future annotation changes. Not part of the Phase 1 feature plan — a standalone infra addition, branched and merged the same way.
