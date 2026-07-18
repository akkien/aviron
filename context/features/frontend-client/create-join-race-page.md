# Create / Join Race Page

## Overview

After logging in, the user lands here to either create a new race or join one by id, then see its current status and participant list — the last piece needed to manually exercise the whole Phase 1 REST flow end to end.

## Requirements

### Page

- Route: `/races` (requires a stored JWT — redirect to `/login` if missing)
- "Create race" form: name + distance, calls `POST /races`, then shows the new race's id
- "Join race" form: race id input, calls `POST /races/{id}/join`
- Below both forms: a race status view — given a race id (from either action above), calls `GET /races/{id}` and lists name, status, distance, and participants (`display_name` + joined time)

### Manual Refresh Only

- No polling or WebSocket in this feature — a "Refresh" button re-fetches `GET /races/{id}`; live updates are Phase 2

## Validation

- Client-side: distance must be a positive number before enabling submit (basic guard, mirrors the server-side check in Create Race)

## Data

- Plain `fetch` calls to `/races`, `/races/{id}/join`, `/races/{id}`, attaching `Authorization: Bearer <token>` from the stored login JWT

## Notes

- Open multiple browser tabs logged in as different users to manually verify join/participant-list behavior, per the project's testing approach (context/project-overview.md §1)
- No styling investment beyond making the three sections legibly separate — this is a test harness, not a product
