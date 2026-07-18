# Login & JWT Issuance

## Overview

Registered users exchange email and password for a JWT they'll use to authenticate every other REST call. This is the second half of the auth flow started in User Registration, and defines the token format every protected endpoint will later verify.

## Requirements

### Endpoint

- `POST /auth/login`
- Request body: `{ "email": string, "password": string }`
- `200` response: `{ "token": string, "expires_at": string (RFC3339) }`
- `401` on wrong email or wrong password — same error for both, don't reveal which one was wrong

### JWT Claims

- `sub` — user id
- `email`
- `exp` — 24h from issuance (short-lived enough for a side project, long enough not to be annoying)
- Signed with HS256 using a secret read from a `JWT_SECRET` env var (via `internal/config`, same `.env` pattern the scaffolding feature set up)

## Validation

- `email` / `password` — both required; no format validation beyond that — a malformed email simply won't match any user and naturally 401s

## Handler

- `internal/auth`, `LoginHandler(pool *pgxpool.Pool, jwtSecret []byte) http.HandlerFunc`
- Registered as `POST /auth/login`

## Data

- `GetUserByEmail(ctx, pool, email) (User, error)`
- Compare the submitted password against `password_hash` with `bcrypt.CompareHashAndPassword`

## Notes

- `JWT_SECRET` needs to be added to `backend/.env` (real value) and `backend/.env.example` (placeholder), following the pattern set in the scaffolding feature
- No refresh tokens in Phase 1 — re-login once the JWT expires
