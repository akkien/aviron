# Current Feature

## Status

Not Started

## Goals

<!-- bullet points of what success looks like -->

## Explain

<!-- bullet points explaining the feature/spec -->

## Plan

<!-- implementation steps, architecture/design notes, tradeoffs -->

## Notes

<!-- constraints, edge cases -->

## History

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
- **User Registration** (2026-07-18) — `POST /auth/register` (bcrypt cost 12, `409 email_taken`, `400` field-keyed validation errors), introducing the layered `handler → service → repository` architecture per domain (documented in context/coding-standards.md, "Backend Architecture") plus `internal/postgres` for driver-specific repos and `internal/httpx` for shared response helpers. Process wiring was reorganized mid-feature: `cmd/server/main.go` now only loads config and calls `app.Run(cfg)`, with DB setup and route registration moved into `internal/app.go` and `internal/httpserver/route.go`. Also planned the rest of Phase 1 into small specs under context/features/ (auth, races, frontend-client) with an index at context/features/phase-1-plan.md. Known cosmetic gap, not blocking: `internal/app.go` declares `package internal` directly instead of living in its own `internal/app/` subdirectory, so `main.go` needs an import alias (`app "github.com/akkien/aviron/internal"`) — worth tidying next time that file is touched. Next feature per context/features/phase-1-plan.md: `auth/login`.
- **Swagger API Docs** (2026-07-18) — Documented `GET /healthz` and `POST /auth/register` with `github.com/swaggo/http-swagger` annotations, generated `backend/docs/` via `swag init`, and served the UI at `GET /swagger/`. Added a `make docs` target to regenerate after future annotation changes. Not part of the Phase 1 feature plan — a standalone infra addition, branched and merged the same way.
- **Login & JWT Issuance** (2026-07-18) — `POST /auth/login` (bcrypt verify, HS256 JWT with `sub`/`email`/`exp` claims, 24h expiry), extending `auth.AuthRepository` with `GetUserByEmail` and collapsing "user not found"/"wrong password" into one `ErrInvalidCredentials` so the response can't be used to enumerate accounts. Renamed the domain's types to match `internal/postgres`'s existing convention (`Handler`/`Service`/`Repository` → `AuthHandler`/`AuthService`/`AuthRepository`, constructors `NewAuthHandler`/`NewAuthService`), moved all request/response DTOs into a new `dtos.go`, and documented both as a standing convention in coding-standards.md for future domain packages (e.g. `race` → `RaceHandler`/`RaceService`/`RaceRepository`). Also fixed a latent `.PHONY` bug in the Makefile that made `make docs` a no-op. **Verification gap carried into master:** `go build`/`go vet`/`gofmt -l .` are clean, but `go test ./... -race` was not run after the rename (skipped per explicit request) — the rename was mechanical with no logic changes, so risk is low, but this is not independently confirmed; run the full suite before building on top of this. Next feature per context/features/phase-1-plan.md: `auth/jwt-middleware`.
