# User Registration

## Overview

New users create an account with an email and password. This is the first REST endpoint in the project and establishes the password-hashing convention every other auth feature builds on.

## Requirements

### Endpoint

- `POST /auth/register`
- Request body: `{ "email": string, "password": string, "display_name": string }`
- `201` response: `{ "id": uuid, "email": string, "display_name": string }` — never returns the password or its hash
- `409` if the email is already registered
- `400` on validation failure

### Password Handling

- Hash with bcrypt (cost factor 12) before storing in `users.password_hash`
- Never log or return the raw password or hash

## Validation

- `email` — required, valid email format, lowercased before storing
- `password` — required, minimum 8 characters
- `display_name` — required, 1–50 characters, trimmed

Return validation failures as `400` with a field-keyed error body, e.g. `{ "errors": { "email": "invalid format" } }`.

## Handler

- `internal/auth`, `RegisterHandler(pool *pgxpool.Pool) http.HandlerFunc`
- Registered as `POST /auth/register` in `internal/httpserver`

## Data

- New query, e.g. `CreateUser(ctx, pool, email, displayName, passwordHash) (User, error)`
- Relies on the `users.email` unique constraint for the 409 case — catch the pgx unique-violation error code rather than pre-checking existence

## Notes

- No email verification flow — out of scope per context/project-overview.md §1 ("no need for complex OAuth")
- No rate limiting on this endpoint yet — acceptable for Phase 1, revisit if it's ever abused
