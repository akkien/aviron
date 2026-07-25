# Ranked Leaderboard — Frontend

## Overview

`ranked-leaderboard.md`'s backend went in with no frontend on purpose
("Deliberately no frontend work in this spec's core scope... a natural,
separate follow-up once this endpoint exists"). Confirmed by search before
writing this spec — there is no ranked/multi-user leaderboard anywhere in
the frontend today: `StatCards.tsx` only ever renders `GET /leaderboard/me`
(the caller's own stats), and no `Table`-shaped or multi-user list
component for leaderboard data exists (`frontend/src/components` has
nothing matching `*leaderboard*` besides `StatCards.tsx` and
`types/leaderboard.ts`). This spec is what actually surfaces `GET
/leaderboard?window=alltime|weekly` in the UI.

## Where it lives

On the Dashboard (`RacesPage.tsx`), alongside `StatCards`/`OpenRacesList`
— both of which already live there as dashboard-relevant, non-race-in-
progress content. Not a separate route: this project has no nav structure
beyond `AppHeader`'s logout button (confirmed by reading it), and adding
one just for a single new section would be more scope than the feature
itself. Placed as a new full-width row **below** the existing
`create/join forms + open races` two-column grid — a ranked list with
several columns of data benefits from the full page width more than being
squeezed into that grid's right column alongside `OpenRacesList`. Exact
placement (full-width row vs. folded into the existing grid) isn't
load-bearing — confirm at `start` if a different arrangement reads better
once actually looked at.

## Design

### `types/leaderboard.ts`

Extend (mirrors `backend/internal/leaderboard/dtos.go` exactly, matching
this file's existing header comment convention):

```typescript
export interface LeaderboardEntry {
  rank: number
  user_id: string
  display_name: string
  races: number
  wins: number
  avg_wpm: number
}

export interface LeaderboardTopResponse {
  window: "alltime" | "weekly"
  entries: LeaderboardEntry[]
}
```

### `components/races/RankedLeaderboard.tsx`

New component, following `OpenRacesList.tsx`'s established shape as the
closest existing precedent (a `Card`-wrapped, polled-or-fetched list of
rows, loading/empty/error states) — **not** polled, unlike
`OpenRacesList`: a leaderboard has no joinability-freshness requirement
the way an open-lobby list does, so it only fetches on mount and whenever
the selected window changes, not on an interval.

- Local state: `window: "alltime" | "weekly"` (default `"alltime"`),
  fetched data, loading/error — same shape `StatCards`/`OpenRacesList`
  already use individually (no shared hook extracted for this, each
  existing list component already has its own local fetch effect, no
  established convention to factor one out).
- Header: `CardTitle` ("Leaderboard") plus a two-`Button` toggle
  (`variant="default"` for the active window, `variant="secondary"` for
  the inactive one — reusing `Button`'s existing variant prop, no new UI
  primitive) — switching windows re-fetches via a `useEffect` dependency
  on `window`.
- Rows: reuse `OpenRacesList`'s row shape (a `div` per entry inside
  `CardContent`, not a `<table>` — this codebase has no `Table` shadcn
  primitive, and adding one for a single list is more surface than this
  feature needs). Each row shows rank, display name, wins, avg WPM — no
  medal icons/emoji or other decoration beyond what's already
  established elsewhere in this app, consistent with `project-overview.md`
  §1's "no need to polish UI/UX" scope note.
- Empty state (`entries.length === 0`, e.g. nobody's finished a race in
  the `weekly` window yet): `"No results yet"` text, mirroring
  `OpenRacesList`'s `"No open races right now — create one!"` pattern.
- Loading state: `"Loading..."` text, matching `StatCards`/`OpenRacesList`.
- Error state: mirrors `StatCards`' catch-and-fall-back-to-empty pattern
  (a failed fetch shows the empty state, not a retry UI) — simpler than
  `OpenRacesList`'s "keep last-known list + inline error," appropriate
  here since there's no already-visible list a transient failure would be
  clearing.

### `RacesPage.tsx`

One import, one new line rendering `<RankedLeaderboard />` below the
existing grid — no other changes to the page.

## Data flow

```text
RankedLeaderboard mounts (or `window` toggled)
  -> apiFetch<LeaderboardTopResponse>(`/leaderboard?window=${window}`)
  -> renders ranked rows, or the empty/loading/error state
```

No WebSocket involved — this is plain REST, matching the backend spec's
own Postgres-only, non-real-time design.

## Testing

Same disclosed gap as every frontend feature in this project's history: no
browser automation tool is available in this environment. Verification is
`yarn build`/`yarn lint` clean, plus code-level reasoning — consistent
with `websocket-client.md`/`reconnect-ui.md`/every UI-revamp feature's
already-established precedent, not a new gap this spec introduces.

## Notes

- Depends on `ranked-leaderboard.md`'s backend already being merged (it
  is, per `current-feature.md`) — this spec is purely additive on the
  frontend, no backend changes.
- Not in scope: any indication of the *current user's own* rank within
  the list (e.g. highlighting their row, or showing "you: #14" if they're
  outside the fetched page) — `GET /leaderboard` doesn't return that
  today, and adding it would mean extending the backend response shape,
  which is out of scope for a frontend-only follow-up. A reasonable
  future addition if ever wanted, not assumed here.
