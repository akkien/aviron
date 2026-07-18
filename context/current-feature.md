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

- **Project Scaffolding & Local Postgres** (2026-07-18) — Go module (`github.com/akkien/aviron/backend`), package layout (`cmd/server`, `internal/config|db|httpserver`), local Postgres via Docker Compose (`postgres:18-alpine`) with the initial schema migration (`golang-migrate`), `GET /healthz`, `.env`-based config (`godotenv`), and `make run`/`make test`. See docs/feature-log.md for details. Next up per the roadmap (context/project-overview.md §12, Phase 1): auth (`POST /auth/register`, `POST /auth/login`).
