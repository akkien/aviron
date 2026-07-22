# Phase 2.6 — Live Pending Lobby

## Overview

Not part of `context/project-overview.md`'s original roadmap — discovered while
investigating a real bug report: after the creator started a race, every
other player had to manually click "Refresh" to see it. Root-causing that
surfaced two separate problems, only one of which is cosmetic:

1. **No push channel for the pending → active transition.** A pending
   player's `raceDetail` is a one-time REST snapshot
   (`GET /races/{id}`, fetched once on mount by `RaceDetailPage.tsx`); the
   only way to learn the race started is a manual "Refresh" click. Polling
   was considered and rejected — it only shrinks the delay window, it
   doesn't remove it.
2. **The delay window is a real, unfair time penalty, not just stale UI.**
   `RoomActor`'s `FinishTimeMs` is computed as
   `p.FinishedAt.Sub(r.startedAt)`, and `r.startedAt` is set once, at room
   creation, shared by every participant. If a player doesn't discover the
   race started until N seconds after the creator does, their official race
   clock has already been running for N seconds before they can press a
   single key — baked directly into their persisted result.

The fix has to be structural: every player must already be holding a live
WebSocket connection to the room *before* the race starts, so the
"race started" transition reaches everyone through the exact same broadcast
mechanism, at the exact same moment (network latency only, not a poll
interval or a manual click). That requires moving the room actor's spawn
point from `POST /races/{id}/start` to race creation, and reconciling every
piece of existing Phase 2 logic that implicitly assumed a room only exists
once a race is already active.

**A second, related problem this phase's own design introduces:** before
this phase, a `pending` race held zero WebSocket connections — nothing to
leak. Once pending players hold live connections from the moment they join,
a lobby the creator never actually starts (distracted, changed their mind,
just forgot) would otherwise hold those connections — and the room actor
goroutine behind them — open indefinitely. This phase adds a bounded pending
lifetime (5 minutes from room creation) to cap that cost, with a frontend
countdown so it's a visible, expected outcome rather than a connection that
mysteriously dies.

## Specs, in build order

1. `room-lifecycle/early-spawn.md` — spawn the room actor at
   `POST /races` instead of `POST /races/{id}/start`; reconcile
   `noShowTimeout`/finish semantics for a room that can now be empty and
   still genuinely `pending` (not just empty-and-abandoned mid-race).
2. `room-lifecycle/pending-connections.md` — let `GET /ws` attach to a
   `pending` room actor, give `RoomActor` an explicit pending/active status,
   and reconcile telemetry-while-pending, the pending "Leave" flow, and
   grace-period/reconnection semantics now that a live connection exists
   before the race is active.
3. `websocket/race-started-broadcast.md` — the actual fairness fix: a new
   `race_started` server → client message, broadcast to every attached
   connection the instant `POST /start` succeeds, carrying `prompt_text`
   directly so there's no follow-up REST round-trip either.
4. `room-lifecycle/pending-expiry.md` — a 5-minute bound on how long a room
   can stay `pending`, reusing (not duplicating) `early-spawn.md`'s
   no-Postgres-write teardown path; exposes a computed expiry timestamp so
   the frontend can render a countdown.
5. `websocket/race-expired-broadcast.md` — the matching `race_expired`
   message, broadcast to every attached connection before the room tears
   down, so expiry is a clean, explained transition rather than a
   connection that silently dies (exactly the failure mode
   `docs/concurrency.md`'s finish-race bugfix already exists to prevent —
   this reuses that same guarantee, not a new one).
6. `frontend/live-lobby.md` — open `useRaceSocket` as soon as a session
   token exists, not gated on the race already being active; handle
   `race_started` and `race_expired`; render the pending-expiry countdown;
   reconcile the pending "Leave" button and the now-redundant "Refresh"
   button.

## Dependency order

- `early-spawn.md` is the foundation — nothing else here is possible until
  a room actor exists before the race starts.
- `pending-connections.md` depends on `early-spawn.md` (there must be a
  room to connect to) and is itself a prerequisite for
  `race-started-broadcast.md` (there must be pending connections to
  broadcast to).
- `pending-expiry.md` depends on `early-spawn.md` (reuses its teardown
  path) and `pending-connections.md` (there must be live connections for
  expiry to matter) — it's independent of `race-started-broadcast.md`,
  since a room either starts or expires, never both.
- `race-expired-broadcast.md` depends on `pending-expiry.md` (there must be
  something server-side deciding to expire before there's anything to
  broadcast) the same way `race-started-broadcast.md` depends on
  `pending-connections.md`.
- `frontend/live-lobby.md` depends on every backend spec above — it has no
  backend work of its own, it's purely the consumer of what they build.

## Explicitly out of scope

- Redis/cross-instance room ownership — still Phase 4
  (`context/project-overview.md` §5), unaffected by moving the spawn point
  earlier within a single instance.
- Any change to how `race_state` broadcasting or telemetry works once a
  race is genuinely `active` — that's Phase 2's existing design, untouched.
  This phase only changes what happens *before* `active`.
- Redesigning grace-period/eviction semantics — reused as-is for a pending
  disconnect, not rebuilt (see `pending-connections.md`).
- Extending or resetting the 5-minute pending lifetime on activity (e.g.
  someone joining resets the clock) — it's a fixed window from room
  creation, not a rolling idle timeout. Revisit only if this turns out to
  feel too aggressive in practice.

## Side benefit, not the goal

Once pending connections exist, the pending lobby's participant list also
starts updating live as people join/leave (`ParticipantJoined` already
triggers an immediate `broadcastSnapshot()`, per `websocket/ws-endpoint.md`)
— worth noting so it isn't mistaken for scope creep when it shows up during
`start`. The actual goal of this phase is the `race_started` fairness fix;
this falls out of the same mechanism for free.
