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
