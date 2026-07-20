# Create / Join Race Page

## Overview

After logging in, the user lands here to create a race, join one, start it, and then type — a shared prompt text moves their car, mirroring the typing-race mechanic in context/project-overview.md §13. This is the last piece needed to manually exercise the whole Phase 1 REST flow end to end; there's no WebSocket yet, so "seeing everyone's car" is limited to your own for now.

## Requirements

### Page

- Route: `/races` (requires a stored JWT — redirect to `/login` if missing)
- "Create race" form: name + target word count (`distance_meters`), calls `POST /races`, then shows the new race's id
- "Join race" form: race id input, calls `POST /races/{id}/join`
- "Start race" button (shown only to the race's creator): calls `POST /races/{id}/start`, stores the returned `prompt_text`
- Below all three: a race status view — given a race id, calls `GET /races/{id}` and lists name, status, target word count, and participants (`display_name` + joined time)

### Typing View (once the race is `active`)

- Fetches the prompt text via `GET /races/{id}/text` if not already held locally (e.g. a participant who joined after start)
- Renders the prompt text with the already-typed portion visually distinct from the remaining portion — a basic diff against the input box's current value, no dedicated typing-test library needed
- A simple car-lane visualization: one row per participant; car position = `(words typed correctly / target word count) * lane width`. Without a WebSocket, only the local player's own car actually moves — the other rows are static placeholders until Phase 2.

### Manual Refresh Only

- No polling or WebSocket in this feature — a "Refresh" button re-fetches `GET /races/{id}`. **Other players' positions do not update live** until Phase 2 adds the WebSocket room actor; this page's typing view only ever animates the local player's own progress in real time.

## Validation

- Client-side: word-count target must be a positive number before enabling submit (basic guard, mirrors the server-side check in Create Race)

## Data

- Plain `fetch` calls to `/races`, `/races/{id}/join`, `/races/{id}/start`, `/races/{id}/text`, `/races/{id}`, attaching `Authorization: Bearer <token>` from the stored login JWT
- Per-word progress (§13: one update per completed word) has nowhere to send to yet in Phase 1 — there's no WebSocket endpoint until Phase 2 (context/project-overview.md §4.2). Track words-correct-so-far in local component state only, and render the local car from that; don't build a REST substitute for the real-time send, it becomes real once the Phase 2 WebSocket client lands.

## Notes

- Open multiple browser tabs logged in as different users to manually verify join/participant-list behavior, per the project's testing approach (context/project-overview.md §1)
- Use shadcn/ui's `Card`/`Input`/`Button` for the create/join/start forms and Tailwind utility classes (flex/grid) to separate sections and lay out the car lanes — still no styling investment beyond that: no custom design system, animation, or theming past shadcn's defaults, this is a test harness, not a product
- See context/project-overview.md §13 for the full typing-race mechanic (why per-word telemetry, why the server doesn't verify typed text, why field names are reused rather than renamed)
