# Live Lobby (Frontend)

## Overview

The frontend half of Phase 2.6: actually hold the live connection the rest
of this phase depends on. `useRaceSocket` opens as soon as a `session_token`
is available — as soon as a player lands on `/races/:raceId`, whether the
race is `pending` or `active` — instead of being gated on
`raceDetail.status === "active"`. Handles the new `race_started` and
`race_expired` messages (`websocket/race-started-broadcast.md`,
`websocket/race-expired-broadcast.md`) to transition local UI state the
instant either arrives, renders the pending-expiry countdown
(`room-lifecycle/pending-expiry.md`'s `pending_expires_at`), and reconciles
the pending "Leave" flow per `room-lifecycle/pending-connections.md`.

## Requirements

### Connection gating

`RaceScreen.tsx`'s current
`useRaceSocket(raceId, isActive ? sessionToken : null)` loses the
`isActive` gate entirely — becomes `useRaceSocket(raceId, sessionToken)`,
opening as soon as a session token exists at all. `session_token` is
already available at this point regardless of race status — `CreateRace`
now auto-joins the creator and returns their own token
(`ui-revamp/race-screen.md`'s "creator should be a player" fix), and it's
threaded through router navigation state to `RaceDetailPage`
(`race-detail-route`'s existing design) — no new token flow needed.

This one line is the structural fix for the fairness problem; every other
spec in this phase exists to make it safe to write.

### Handling `race_started`

`useRaceSocket.ts`'s `ws.onmessage` gains a `race_started` case alongside
the existing `race_state`/`race_finished`:

- Sets local `promptText` state directly from the message body — no
  `GET /races/{id}/text` fetch needed for a client that was already
  connected when `start` happened.
- Signals that the race is now active, so `RaceScreenSidebar` renders its
  active view (leaderboard + `TypingBox`) instead of the pending view. Exact
  shape (a new returned field from the hook, vs. deriving it from
  `raceState`/`promptText` already being non-null) is an implementation
  detail for `start`, not specified here.

`RaceScreen.tsx`'s existing `GET /races/{id}/text` effect (gated on
`isActive`, derived from `raceDetail.status`) stays as the fallback for a
client that loads the page or reconnects *after* `start` already happened —
`race_started` only short-circuits it for a client that was already
connected, per `race-started-broadcast.md`'s Notes.

### Pending-expiry countdown

`RaceStatusResponse` (`types/race.ts`) gains `pending_expires_at: string | null`,
mirroring `pending-expiry.md`'s new REST field. While `status === "pending"`
and `pending_expires_at` is set, `RaceScreenSidebar`'s pending view renders
a live countdown to it (`mm:ss` remaining, ticking down once a second —
computed client-side from the deadline, no server round-trip needed per
tick).

**Open question for `load`:** shown to every pending player, or only the
creator? Leaning toward showing it to everyone — only the creator can
actually prevent expiry by starting the race, but a countdown that appears
for the creator and not for anyone else would make the room's eventual
disappearance look unexplained to everyone but them, which is exactly the
"connection dies with no explanation" problem this whole phase exists to
avoid. A creator-only visual emphasis (e.g. "Start before it closes!") on
top of a countdown everyone can see is a reasonable middle ground, worth
confirming before `start`.

### Handling `race_expired`

`useRaceSocket.ts`'s `ws.onmessage` gains a `race_expired` case, exposed as
a new state distinct from the hook's existing `evicted` (which specifically
means "reconnect grace period lapsed after a disconnect" —
`reconnection/grace-period.md` — a different condition entirely) and
`leaving` (a self-initiated quit). `RaceScreenSidebar` renders a new terminal
state for it ("This race wasn't started in time and has been closed."),
and — per this project's existing redirect-on-quit pattern
(`race-detail-route`'s `onLeftRace`) — sends the player back to `/races`,
reusing that same callback rather than inventing a second redirect path.

### Leave button reconciliation

`POST /races/{id}/leave` no longer exists —
`room-lifecycle/pending-connections.md` removed it entirely, unifying
leave onto the WebSocket `leave_race` message for both pending and active
races, with the room actor deciding server-side what that means. There's
no "prefer WS, fall back to REST" — REST isn't an option anymore.

Pending "Leave" (`RaceScreenSidebar.handleLeavePending`) becomes a call to
the same `leaveRace()` the active-race Quit button already calls from
`useRaceSocket` — at that point the two buttons' handlers are functionally
identical, so `handleLeavePending` as a separate function may not need to
exist at all (an implementation detail for `start`, not specified here).
`LeaveRaceResponse` (`types/race.ts`) and the `apiFetch` call it backed
become dead code, removed alongside this.

### The "Refresh" button

The pending player list's "Refresh" button — today the *only* way to
discover new joiners or that the race started (see: the bug report this
whole phase exists to fix) — becomes redundant for both purposes once this
spec lands: participant-list updates arrive live via `race_state`
(`pending-connections.md`'s side benefit), and race-start arrives via
`race_started`.

**Open question for `load`:** remove the button entirely, or keep it as a
manual "force resync" fallback for some edge case (e.g. a missed broadcast
during a brief reconnect gap)? Leaning toward removing it — an
always-visible button for a problem that no longer structurally exists
reads as confusing, not reassuring, and this project's conventions favor
deleting things once they're genuinely unused over leaving them "just in
case." Flagging since it's a visible UI change worth confirming before
`start`, not assuming.

## Notes

- Depends on every backend spec in this phase
  (`room-lifecycle/early-spawn.md`, `room-lifecycle/pending-connections.md`,
  `websocket/race-started-broadcast.md`, `room-lifecycle/pending-expiry.md`,
  `websocket/race-expired-broadcast.md`) — this spec has no backend work of
  its own, it's purely the consumer of what they build.
- No change to `TypingBox.tsx`, `RaceTrack.tsx`, or any rendering logic
  beyond what's described above — this phase is about *when* state arrives,
  not how it's displayed once it has.
- Same disclosed limitation as every frontend spec in this project: no
  browser automation tool is available in this environment; verification
  will be build/lint plus code-level reasoning, the same bar as every prior
  frontend feature.
