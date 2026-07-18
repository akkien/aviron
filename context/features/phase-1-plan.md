# Phase 1 — Foundation Plan

## Overview

Phase 1 (context/project-overview.md §12) delivers the REST + Postgres foundation: users register and log in, create a race, join a race, and check its status — all over plain REST, no real-time behavior yet. A minimal React client exercises the whole flow end to end. WebSocket, live sync, reconnection, and the leaderboard all depend on Phase 2's room actor and are explicitly deferred.

## Features, in build order

1. ✅ **Project Scaffolding & Local Postgres** — done (see docs/feature-log.md)
2. **Auth** (folder: `auth/`) — big feature, split into three
   1. `auth/user-registration.md` — `POST /auth/register`
   2. `auth/login.md` — `POST /auth/login`, JWT issuance
   3. `auth/jwt-middleware.md` — shared middleware every protected endpoint uses
3. **Races** (folder: `races/`) — big feature, split into three
   1. `races/create-race.md` — `POST /races`
   2. `races/join-race.md` — `POST /races/{id}/join`, session token
   3. `races/race-status.md` — `GET /races/{id}`
4. **Frontend Client** (folder: `frontend-client/`) — split into two
   1. `frontend-client/login-page.md`
   2. `frontend-client/create-join-race-page.md`

## Dependency order

- Login and the JWT middleware block every Races endpoint (they all require an authenticated caller).
- Join Race and Race Status both depend on Create Race existing.
- Each frontend page depends on its corresponding backend endpoint(s) being done.
- Recommended build order: Auth (3 features) → Races (3 features) → Frontend Client (2 features).

## Explicitly out of scope for Phase 1

- WebSocket, the room actor, live race sync (Phase 2)
- Reconnection / grace period (Phase 2 — depends on the WebSocket connection existing at all)
- `GET /leaderboard` — meaningless until races can actually finish, which needs Phase 2's race-completion logic; revisit once that lands
- Redis, Kafka, Kubernetes (Phase 4)
- Race status ever leaving `pending` — that transition is driven by the room actor in Phase 2, not built here
