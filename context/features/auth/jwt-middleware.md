# JWT Auth Middleware

## Overview

Every endpoint from here on (races, eventually leaderboard) needs to know which user is calling. This feature adds one shared middleware that verifies the JWT issued by Login and makes the user id available to handlers, so no endpoint re-implements token parsing.

## Requirements

### Behavior

- Reads the `Authorization: Bearer <token>` header
- Verifies signature and expiry against `JWT_SECRET`
- On success: attaches the user id to the request context, calls the next handler
- On missing, malformed, or expired token: responds `401` with `{ "error": "unauthorized" }` and does not call the next handler

### Applied To

- All `/races/*` endpoints (Create Race, Join Race, Race Status)
- Not applied to `/auth/register`, `/auth/login`, `/healthz`

## Validation

- A malformed `Authorization` header (missing `Bearer ` prefix, empty token) is treated the same as an invalid token — `401`, no distinction in the response body

## Handler

- `internal/auth`, `RequireAuth(jwtSecret []byte) func(http.Handler) http.Handler` — standard middleware signature, wraps a handler
- A small `UserIDFromContext(ctx) (uuid.UUID, bool)` helper so downstream handlers can read the authenticated user id back out

## Data

- No DB access — pure JWT signature/expiry verification against the claims set by Login

## Notes

- Go 1.22's `http.ServeMux` has no built-in middleware chaining — wrap handlers manually with a small local helper; no router dependency needed yet (same call made in the scaffolding feature)
