# Phase 1 — Foundation Plan

## Overview

Phase 1 (context/project-overview.md §12) delivers the REST + Postgres foundation: users register and log in, create a race, join a race, start it, and check its status — all over plain REST, no real-time behavior yet. The race itself is a typing race (context/project-overview.md §13): players race cars driven by words typed correctly against a shared prompt. A minimal React client exercises the whole flow end to end. WebSocket, live sync, reconnection, and the leaderboard all depend on Phase 2's room actor and are explicitly deferred.

## Features, in build order

1. ✅ **Project Scaffolding & Local Postgres** — done (see docs/feature-log.md)
2. **Auth** (folder: `auth/`) — big feature, split into three
   1. `auth/user-registration.md` — `POST /auth/register`
   2. `auth/login.md` — `POST /auth/login`, JWT issuance
   3. `auth/jwt-middleware.md` — shared middleware every protected endpoint uses
3. **Races** (folder: `races/`) — big feature, split into four
   1. `races/create-race.md` — `POST /races`
   2. `races/join-race.md` — `POST /races/{id}/join`, session token
   3. `races/start-race.md` — `POST /races/{id}/start` + `GET /races/{id}/text` (generates the shared typing-race prompt)
   4. `races/race-status.md` — `GET /races/{id}`
4. **Frontend Client** (folder: `frontend-client/`) — split into two
   1. `frontend-client/login-page.md`
   2. `frontend-client/create-join-race-page.md`

## Dependency order

- Login and the JWT middleware block every Races endpoint (they all require an authenticated caller).
- Join Race, Start Race, and Race Status all depend on Create Race existing.
- Each frontend page depends on its corresponding backend endpoint(s) being done.
- Recommended build order: Auth (3 features) → Races (4 features) → Frontend Client (2 features).

## Explicitly out of scope for Phase 1

- WebSocket, the room actor, live race sync (Phase 2)
- Reconnection / grace period (Phase 2 — depends on the WebSocket connection existing at all)
- `GET /leaderboard` — meaningless until races can actually finish, which needs Phase 2's race-completion logic; revisit once that lands
- Redis, Kafka, Kubernetes (Phase 4)
- Race status ever reaching `finished` (ranking, finish times) — that still depends on Phase 2's live progress tracking. `pending` → `active` **is** in scope now, via `races/start-race.md`'s REST-only `POST /races/{id}/start` — this is a change from the original plan, which assumed status only ever moved via Phase 2's room actor.
