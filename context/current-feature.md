# Current Feature: JWT Auth Middleware

## Status

In Progress

## Goals

- `Auth(jwtSecret []byte) func(http.Handler) http.Handler` wraps a handler so it only runs for requests with a valid, unexpired, correctly-signed `Authorization: Bearer <token>` header
- Missing, malformed, or invalid/expired tokens get `401 {"error":"unauthorized"}` and the wrapped handler never runs
- On success, the wrapped handler can read the authenticated user id back out via `UserIDFromContext(ctx) (string, bool)`
- `go test ./... -race` passes for the new package, including tests covering valid/missing/malformed/wrong-signature/expired tokens

## Explain

- Pure middleware, no DB access — verifying a JWT never needs a database round-trip, unlike `Login`
- Verifies tokens using the exact `jwt.MapClaims`/HS256 shape `auth.AuthService.Login` already signs, so the two features close the loop with each other, without this package importing `internal/auth` at all — it only needs `jwtSecret []byte` and generic JWT claims, no domain types
- The spec's suggested `UserIDFromContext(ctx) (uuid.UUID, bool)` uses `uuid.UUID`; this codebase has treated ids as plain `string` everywhere so far (`auth.User.ID`, `registerResponse.ID`, etc. — populated directly from Postgres's UUID column via pgx's stdlib string scanning, no `github.com/google/uuid` dependency anywhere). Diverging from the spec to return `(string, bool)` instead, for consistency, rather than adding a UUID dependency for one function.
- **Moved mid-feature, per explicit request:** originally implemented as `internal/auth/middleware.go` (`auth.RequireAuth`); relocated to its own `internal/middleware` package (`middleware.Auth`) since the logic has zero dependency on `internal/auth`'s types — a JWT-verifying middleware is a cross-cutting concern, not part of the auth domain's handler/service/repository layering. File renamed `auth.go`/`auth_test.go` within that package (the name describes what the file does — verifies an *auth* token — not the package it lives in).
- Since the test package no longer shares a directory with `internal/auth`, it can't reuse `helpers_test.go`'s `newFakeRepository()` to mint a real login token; the valid-token test now signs a JWT directly with `jwt.NewWithClaims(...)` instead of going through `AuthService.Login` — this is actually more correct, since the middleware's contract is "verify this JWT shape," not "integrate with the auth package."
- **Not wired into any route in this feature** — there's nothing to protect yet; `/races/*` doesn't exist until `races/create-race` (next-but-one). `internal/httpserver/route.go` is unchanged. The next feature that adds a protected endpoint calls `middleware.Auth(jwtSecret)(handler)` around it.

## Plan

### New files

```text
backend/
  internal/
    middleware/
      auth.go               # Auth, UserIDFromContext, unexported context key
      auth_test.go
```

### `internal/middleware/auth.go`

```go
package middleware

import (
    "context"
    "net/http"
    "strings"
    "time"

    "github.com/golang-jwt/jwt/v5"

    "github.com/akkien/aviron/internal/httpx"
)

type contextKey int

const userIDContextKey contextKey = iota

// Auth wraps a handler so it only runs for requests carrying a valid,
// unexpired JWT signed with jwtSecret. On success, the authenticated user id
// is attached to the request context (read it back with UserIDFromContext).
func Auth(jwtSecret []byte) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            const prefix = "Bearer "
            authHeader := r.Header.Get("Authorization")
            if !strings.HasPrefix(authHeader, prefix) {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            tokenString := strings.TrimPrefix(authHeader, prefix)
            if tokenString == "" {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
                if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
                    return nil, jwt.ErrTokenSignatureInvalid
                }
                return jwtSecret, nil
            })
            // jwt.Parse's default validator already rejects an expired token
            // (it parses into jwt.MapClaims, which implements
            // GetExpirationTime(), so err would already be non-nil here) —
            // but check explicitly too, so the expiry guarantee is visible
            // here rather than resting on an implicit library default.
            if err != nil || !token.Valid {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            claims, ok := token.Claims.(jwt.MapClaims)
            if !ok {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            expiresAt, err := claims.GetExpirationTime()
            if err != nil || expiresAt == nil || expiresAt.Before(time.Now()) {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            userID, ok := claims["sub"].(string)
            if !ok || userID == "" {
                httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
                return
            }

            ctx := context.WithValue(r.Context(), userIDContextKey, userID)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// UserIDFromContext reads the user id Auth attached to ctx.
func UserIDFromContext(ctx context.Context) (string, bool) {
    id, ok := ctx.Value(userIDContextKey).(string)
    return id, ok
}
```

Uses an unexported `contextKey` type (not a bare `string`) so this package's context value can never collide with a key set by another package — standard Go idiom for context keys. The explicit `GetExpirationTime()`/`Before(time.Now())` check is technically redundant with `jwt.Parse`'s default validator (which already rejects an expired token via `err`), but it makes the expiry guarantee visible in this function instead of resting on an implicit library default — added after review flagged that the earlier draft looked like it might be missing an expiry check.

### Tests (`auth_test.go`)

- A small `signToken(t, secret, exp)` helper mints a JWT directly with `jwt.NewWithClaims(...)` — used by every test below, since this package has no dependency on `internal/auth` to go through a real `Login` flow
- `TestAuth_ValidToken` — valid token, wraps a dummy handler that echoes `UserIDFromContext` back in the response body, drives the request through `Auth`, asserts `200` and the correct id echoed
- `TestAuth_MissingHeader` — no `Authorization` header → `401`
- `TestAuth_MalformedHeader` — header without the `Bearer` prefix, and `Bearer` with an empty token → `401` (table-driven)
- `TestAuth_InvalidSignature` — a token signed with a different secret → `401`
- `TestAuth_ExpiredToken` — a token minted with `exp` in the past → `401`
- Every `401` case also asserts the wrapped handler was never invoked (a sentinel bool flipped inside it, checked after the request)

### New dependency

- None — reuses `github.com/golang-jwt/jwt/v5`, already added for Login

No divergence from context/project-overview.md; divergences are from the feature spec itself (`uuid.UUID` → `string`, and the package location — both explained in Explain above).

## Notes

- Applying this to real routes happens in `races/create-race` (next-but-one): a protected route becomes `server.Handle("POST /races", middleware.Auth(jwtSecret)(http.HandlerFunc(raceHandler.Create)))` — Go 1.22's `http.ServeMux` has no middleware chaining, so each protected route is wrapped individually (or via a small local helper if that gets repetitive across several routes)
- Next feature per context/features/phase-1-plan.md: `races/create-race`

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
- **User Registration** (2026-07-18) — `POST /auth/register` (bcrypt cost 12, `409 email_taken`, `400` field-keyed validation errors), introducing the layered `handler → service → repository` architecture per domain (documented in context/coding-standards.md, "Backend Architecture") plus `internal/postgres` for driver-specific repos and `internal/httpx` for shared response helpers. Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`, with DB setup and route registration moved into `internal/app.go` and `internal/httpserver/route.go`. Also planned the rest of Phase 1 into small specs under context/features/ (auth, races, frontend-client) with an index at context/features/phase-1-plan.md. Known cosmetic gap, not blocking: `internal/app.go` declares `package internal` directly instead of living in its own `internal/app/` subdirectory, so `main.go` needs an import alias (`app "github.com/akkien/aviron/internal"`) — worth tidying next time that file is touched. Next feature per context/features/phase-1-plan.md: `auth/login`.
- **Swagger API Docs** (2026-07-18) — Documented `GET /healthz` and `POST /auth/register` with `github.com/swaggo/http-swagger` annotations, generated `backend/docs/` via `swag init`, and served the UI at `GET /swagger/`. Added a `make docs` target to regenerate after future annotation changes. Not part of the Phase 1 feature plan — a standalone infra addition, branched and merged the same way.
- **Login & JWT Issuance** (2026-07-18) — `POST /auth/login` (bcrypt verify, HS256 JWT with `sub`/`email`/`exp` claims, 24h expiry), extending `auth.AuthRepository` with `GetUserByEmail` and collapsing "user not found"/"wrong password" into one `ErrInvalidCredentials` so the response can't be used to enumerate accounts. Renamed the domain's types to match `internal/postgres`'s existing convention (`Handler`/`Service`/`Repository` → `AuthHandler`/`AuthService`/`AuthRepository`, constructors `NewAuthHandler`/`NewAuthService`), moved all request/response DTOs into a new `dtos.go`, and documented both as a standing convention in coding-standards.md for future domain packages (e.g. `race` → `RaceHandler`/`RaceService`/`RaceRepository`). Also fixed a latent `.PHONY` bug in the Makefile that made `make docs` a no-op. **Verification gap carried into master:** `go build`/`go vet`/`gofmt -l .` are clean, but `go test ./... -race` was not run after the rename (skipped per explicit request) — the rename was mechanical with no logic changes, so risk is low, but this is not independently confirmed; run the full suite before building on top of this. Next feature per context/features/phase-1-plan.md: `auth/jwt-middleware`.
