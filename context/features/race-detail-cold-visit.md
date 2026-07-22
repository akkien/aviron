# Race Detail — Cold Visit & Spectator View

## Overview

Not part of any existing phase plan — discovered from a direct bug report
after Phase 2.6 shipped. Reloading the page right after finishing a race
shows "You were disconnected too long and have left the race." (the
`evicted` state); pasting the race's URL into a fresh tab shows a permanent
"Loading prompt..." Both trace back to the same root cause: the race page
only ever renders a correct experience for a client holding a live,
successfully-connected WebSocket — anything else (a reload after the race
already ended, a cold link, a spectator who never joined) falls into a
misleading or stuck state instead of a plain, correct read-only view of
what `GET /races/{id}` already knows.

Explicit requirement for this fix, stated directly: **the race page must
render normally in both cases — reload/cold-link, and regardless of
whether the visitor was ever a participant in the race.**

## Requirements

### Backend: expose finish results over REST

`race_participants.finish_rank`/`finish_time_ms`/`avg_pace_watt` are
already written the instant a race finishes (`FinishRace`'s transaction,
`internal/postgres/race_repository.go`), but `GetRaceWithParticipants`'s
query (same file) only ever selects `user_id, display_name, joined_at` —
confirmed directly, not assumed. That means `GET /races/{id}` can never
show results, even for a race that finished minutes or days ago; only a
client that was live-connected at the exact moment of finishing ever sees
them, via the WebSocket `race_finished` message.

- `internal/race/race.go`'s `Participant` struct gains `FinishRank *int`,
  `FinishTimeMs *int64`, `AvgPaceWatt float64` — same nullable shape as
  `internal/room/finish.go`'s existing `RaceResultJSON`, not a new
  convention.
- `GetRaceWithParticipants`'s query additionally selects
  `rp.finish_rank, rp.finish_time_ms, rp.avg_pace_watt` — always, not
  conditionally on status; these columns are simply `NULL` for a
  pending/active race's participants, which is already this project's
  established "nil means N/A" convention.
- `internal/race/dtos.go`'s `participantResponse` gains the same three
  fields, JSON tags matching `RaceResultJSON` exactly
  (`finish_rank`/`finish_time_ms`/`avg_pace_watt`) so the frontend can
  reuse one shape for both the live WS message and the REST response.
- `RaceHandler.Status` needs no other change — it already passes
  `detail.Participants` through field-by-field into `participantResponse`.

### Frontend: never attempt a WebSocket connection to a race that's already over

`RaceScreen.tsx` currently connects `useRaceSocket` whenever `sessionToken`
is non-null, with no gate on race status at all (`live-lobby.md` removed
the previous `isActive` gate entirely). That's what causes the reload bug:
the browser preserves router navigation state across a same-tab reload, so
`sessionToken` is often still present after a race finishes — the hook
dutifully tries to reconnect to a room whose actor has already
self-cancelled and been removed from the registry, the handshake 404s,
the existing 3-attempt reconnect loop runs out, and the client lands on
`evicted` — a real, working mechanism doing the wrong thing for this case,
not a broken one.

- Gate on **terminal** status specifically, not on "known good" status —
  `const terminal = raceDetail?.status === "finished" || raceDetail?.status === "cancelled"`,
  passing `terminal ? null : sessionToken` into `useRaceSocket`. Gating on
  `raceDetail?.status === "pending" || "active"` instead would be wrong: a
  freshly created/joined race has `raceDetail === null` for the brief
  window before its first `GET /races/{id}` resolves, and that must still
  connect immediately — this is exactly the fairness property
  `race-started-broadcast.md`/`live-lobby.md` were built to guarantee.
  Failing open (connect unless we positively know it's over) preserves
  that; failing closed (connect only once we positively know it's fine)
  would silently reintroduce the same kind of connection-delay unfairness
  those specs closed.
- A race that's active when this page loads but finishes before a reload
  happens is unaffected by this change — nothing currently re-fetches
  `raceDetail` on a live `race_finished` message (an existing, accepted
  staleness noted directly in `RaceScreenSidebar.tsx`'s own comment), so
  `terminal` only ever flips true from a fresh REST fetch, never out from
  under an actively-finishing connection.

### Frontend: render finished results from REST, not just from the live message

`RaceScreenSidebar.tsx`'s `if (finished) {...}` branch only ever renders
from the hook's local `finished` state — set exclusively by a live
`race_finished` WebSocket message. A cold visit (reload, fresh link, a
spectator) never receives that message, so this branch never fires; the
component instead falls all the way through to the generic
`promptText === null` fallback, which is what produces "Loading prompt..."
for a race that's actually over.

- Condition becomes `if (finished || raceDetail.status === "finished")`.
- Results source: prefer `finished.results` when present (the live path —
  fresher than any REST snapshot, and already working correctly today for
  a client connected at the exact moment of finishing); otherwise build
  the equivalent ranked list from `raceDetail.participants`, now carrying
  the same `finish_rank`/`finish_time_ms`/`avg_pace_watt` fields the REST
  backend change above adds. One rendering path, two data sources — not a
  second, parallel results UI to maintain.

### Frontend: a correct view for a visitor who isn't a participant

`GET /races/{id}` and `GET /races/{id}/text` already work for any
authenticated user regardless of participation or session token —
confirmed directly: `apiFetch` only ever attaches the main JWT
(`lib/auth.ts`'s `getToken()`), never a per-race `session_token`, and
neither endpoint requires the latter. The pending and finished views
described above are therefore already spectator-safe once built. Two
concrete gaps remain:

- **Pending lobby's "Leave" button is shown unconditionally today** —
  confirmed directly, unlike "Start Race" which already correctly checks
  `isCreator`. Gate it on
  `isParticipant = raceDetail.participants.some(p => p.user_id === currentUserId)`
  — a spectator has nothing to leave.
- **An active race with no session token today still falls through to the
  full interactive leaderboard + `TypingBox`** (`GET /races/{id}/text`
  succeeds regardless of participation, so `promptText` does get set) —
  `TypingBox` has no read-only/disabled concept at all (confirmed
  directly: it always auto-focuses and always calls `sendTelemetry` on
  every completed word, with no gating prop), so a spectator gets a fully
  interactive typing box that silently does nothing, which is confusing,
  not "normal." For `raceDetail.status === "active" && !isParticipant`:
  render the same leaderboard block participants already see (it's
  already spectator-safe — `RaceTrack.tsx` and the sidebar's leaderboard
  both degrade to 0% cleanly with no live `raceState`, confirmed directly,
  not something this spec needs to touch) with a small "Spectating" label
  in place of the `TypingBox`/"Quit Race" button, and skip the
  `promptText`-dependent branches entirely — a spectator doesn't need
  prompt text at all, so there's no reason for them to ever see "Loading
  prompt..." while it's being fetched for no reason.

## Data

```go
// internal/race/race.go — Participant gains:
FinishRank   *int
FinishTimeMs *int64
AvgPaceWatt  float64
```

```go
// internal/race/dtos.go — participantResponse gains:
FinishRank   *int    `json:"finish_rank"`
FinishTimeMs *int64  `json:"finish_time_ms"`
AvgPaceWatt  float64 `json:"avg_pace_watt"`
```

## Notes

- No migration needed — `race_participants.finish_rank`/`finish_time_ms`/
  `avg_pace_watt` already exist and are already populated by
  `FinishRace`'s transaction; this only changes what's read back out.
- Out of scope: distinguishing *why* a spectator can't type (never joined,
  vs. joined then reloaded without a session token) — both look identical
  from the server's perspective (no valid per-race credential) and should
  look identical here too, matching this project's existing
  no-speculative-detail precedent (`race-expired-broadcast.md`'s "no
  reason field").
- Out of scope: making a spectator's "Spectating" leaderboard update live
  via WebSocket without a session token — that would require issuing
  read-only credentials to non-participants, a real scope expansion this
  bug report didn't ask for. A spectator's view is a REST snapshot,
  refreshable by reloading, same as the pending lobby already is for a
  non-participant today.
- Verification should include: a repository/service-level test that
  `GetRaceWithParticipants`/`GetRaceDetail` returns non-nil finish fields
  for a finished race's participants; a frontend check (code-level
  reasoning, per this project's disclosed no-browser-automation
  limitation) that reloading a finished race's URL renders results instead
  of reattempting a connection, and that a non-participant's active-race
  view never mounts `TypingBox`.
