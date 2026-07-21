# Leave Race

## Overview

Not part of `context/project-overview.md`'s original spec or Phase 2's original roadmap — added by explicit request after Phase 2's backend was otherwise complete. Every path into a race currently only ever adds a participant; there's no way to voluntarily back out. This covers two distinct scenarios that need different mechanisms:

1. **Leaving before a race starts** — REST-only, since no room actor exists yet for a `pending` race.
2. **Leaving mid-race** — WebSocket-only, and *immediate*, not gated by `reconnection/grace-period.md`'s 30s reconnect window. A disconnect might be a dropped connection the player wants to come back from; an explicit "leave" message means they've already decided they're not coming back, so there's nothing to wait for.

It also changes `race-completion/finish-race.md`'s rank-assignment rule: today, a participant who never reaches the target simply vanishes from the finish results the moment they're removed from `participants` (via grace-period expiry) — no rank recorded at all. This spec makes leaving (voluntary or via grace-period exhaustion) produce an explicit result instead of silence: everyone who didn't finish shares one rank, equal to the total number of participants who were ever part of the race. A 5-player race where nobody finishes and everyone quits means every one of them is recorded at rank 5 — tied for last, not individually ordered by when they quit.

**Naming, decided after discussion**: the existing `RoomEvent` named `ParticipantLeft` (`reconnection/grace-period.md`'s grace-period-timeout removal, `internal/room/events.go`) is **renamed to `ParticipantEvicted`** — matching this codebase's own established vocabulary for that state (`RoomActor.evicted`, `IsEvicted`), not a new metaphor. `ParticipantLeft` is freed up for this feature's intentional-quit event instead, since it's the name that actually reads correctly for "the player left the race." The two events stay functionally distinct (see below) — only the labels moved, not the reasoning behind keeping them separate.

## Requirements

### Leaving Before Start (REST)

- New endpoint on `internal/race`: removes the caller as a participant. **Open question for `start`**: HTTP verb/path. `DELETE /races/{id}/leave` is more REST-correct for "remove myself," but this project's existing `POST /races/{id}/join` (a POST for what's arguably a sub-resource creation) sets a "verb over pure REST-correctness" precedent worth staying consistent with — `POST /races/{id}/leave` is the other reasonable option. Pick one at `start`, not both.
- Only valid while `race.status == 'pending'` — mirrors `JoinRace`'s own pending-only rule. Once active, leaving must go through the WebSocket path below; `409 race_not_pending` otherwise (reusing the existing sentinel error, not a new one).
- Unlike every other participant-related write so far, this is a real `DELETE` from `race_participants` — before a race starts there's no result to preserve, leaving pre-start means "I was never really racing."
- New sentinel error needed: caller isn't currently a participant (never joined, or already left) — `ErrNotParticipant`, `404`.
- **Accepted edge case, not solved here**: the creator leaving their own race. `races.created_by` isn't nullable and isn't touched by this — the race row and its creator reference stay intact even if the creator removes their own `race_participants` row. A creator-less-participant race that later gets `start`ed is a slightly odd but harmless state; not worth guarding against for a side project.

### Leaving During an Active Race (WebSocket)

- New client message, symmetric with `join_race`: `{"type":"leave_race"}`. Added to `websocket/protocol.md`'s `ClientMessage`/`decodeClientMessage`/`toRoomEvent`.
- Decodes to a `RoomEvent` named `ParticipantLeft` — the name freed up by the rename above. This is still a genuinely separate event from the (renamed) `ParticipantEvicted`, not a reuse with a flag: `ParticipantEvicted` stays guarded on `DisconnectedAt != nil` (protects against a stale timer racing an in-flight reconnect — already found and fixed once in `reconnection/grace-period.md`), and that guard's meaning shouldn't change. A still-connected participant sending an intentional leave is exactly the case that guard exists to reject if misapplied, so `ParticipantLeft` (this feature) carries no such guard at all — it always honors the request. Unlike the join/reconnect case (where reusing `ParticipantJoined` and branching on existing state was the natural fit), there's no existing state here to branch on that cleanly separates "quit" from anything else — keeping this as its own event, just under the more fitting name, is the better fit here.
- Applying this event removes the participant from the room **immediately** — no timer, no grace period, no reconnect window. They should also be added to the `evicted` set (`reconnection/grace-period.md`'s mechanism), so `IsEvicted` rejects a later reconnect attempt (e.g. accidentally reopening the tab) the same way it already rejects a too-late grace-period reconnect.
- Must call the same finish-condition check every other participant-removing event already calls — quitting could be the event that empties the room, or the one that leaves everyone else terminal.

### Renaming the Existing `ParticipantLeft` Event

This is a rename of already-shipped, already-tested code, not just new code landing alongside it — worth its own checklist so it doesn't get treated as an afterthought during `start`:

- `internal/room/events.go`: the type itself, `ParticipantLeft` → `ParticipantEvicted`.
- `internal/room/room.go`: `applyEvent`'s case for it, the `time.AfterFunc` callback in the `ParticipantDisconnected` case that sends it, and `checkRaceFinished`'s doc comment (which currently names `ParticipantLeft` as one of its callers).
- Every existing test that references the old name across two already-completed features' test suites — at minimum `TestRoomActor_ApplyEvent_ParticipantLeft_RemovesAndEvicts`, `TestRoomActor_ApplyEvent_ParticipantLeft_StaleEventIgnoredAfterReconnect`, `TestRoomActor_ApplyEvent_ParticipantLeft_UnknownParticipant` (`reconnection/grace-period.md`), and `TestRoomActor_ApplyEvent_ParticipantLeft_EmptyingRoomTriggersFinish` / `TestRoomActor_ApplyEvent_ParticipantLeft_DoesNotFinishIfOthersStillRacing` (`race-completion/finish-race.md`) — renamed to match, not left stale.
- `internal/ws/endpoint_integration_test.go`'s `TestIntegration_RejectsReconnectAfterGracePeriodExpired`, which directly constructs `room.ParticipantLeft{...}` to simulate an expiry without waiting the real 30s.
- No behavior change from the rename itself — pure find-and-rename, verified the same way any other feature's changes are (`go build`/`go vet`/`go test ./... -race`), separately from whatever the rename is bundled with once this is actually implemented.

### Rank Assignment for Quitters (updates `race-completion/finish-race.md`)

- **Current behavior**: `checkRaceFinished`'s results-building loop only reads from the live `participants` map. Anyone removed before finishing — via grace-period expiry (the renamed `ParticipantEvicted`) today, or the new `ParticipantLeft` quit event — is already gone from that map by the time results are built, so they're silently excluded from the finish transaction entirely. Their `race_participants` row is left untouched (same `NULL` rank forever as someone who registered but never connected).
- **New rule**: anyone who leaves without finishing — grace-period exhaustion or an intentional quit, both — gets a rank, not silence: a single shared value equal to the total number of distinct participants who ever joined the room. Not finishing order, not sequential per-quitter ranks — every non-finisher ties at the same bottom rank.
- **This requires no longer discarding a departed participant's state outright.** Both `ParticipantEvicted`'s handling and the new `ParticipantLeft` event currently (or would, if built the same way) `delete()` the entry from `participants` with nothing kept. This spec needs *some* record to survive long enough to produce a `ParticipantResult` for them at finish time — e.g. a second, smaller map (`departedParticipants`) that removal moves entries into instead of discarding, unioned with the live `participants` map when `checkRaceFinished` builds its final results list. Exact shape is a `start`-time decision, not fixed here.
- **"Total number of participants" needs its own source.** Nothing on `RoomActor` currently counts this. Proposed: a running counter (e.g. `totalParticipants int`), incremented once per genuinely new `ParticipantJoined` (the "unknown participant" branch specifically — not reconnects, not the duplicate-while-connected case) — the room's own natural notion of "how many distinct people ever raced here." Deliberately not the REST-registered `race_participants` count: someone who joined the lobby but never connected live is already invisible to the room actor everywhere else in this codebase (`race-completion/finish-race.md`'s own note on this), and this shouldn't be the one place that changes.
- **Real behavior change to flag, not just an addition**: under `race-completion/finish-race.md` as shipped, an abandoned race (everyone leaves, nobody finishes) produces an *empty* results slice — nobody gets a persisted result. Under this spec, if departed participants stay tracked, the same scenario now produces a full result set — everyone who ever joined, all tied at the same bottom rank. Arguably more correct (a race five people actually played now has five recorded results, not zero), but it's a behavior change to the feature that just shipped, not a pure addition — call this out explicitly when this spec is loaded for `start`.

## Concurrency

- Neither new trigger introduces a genuinely new concurrency pattern. The REST leave is a single `DELETE ... WHERE race_id = $1 AND user_id = $2`, the same shape as `AddParticipant`'s insert — no transaction needed (one statement, one row).
- The WS `leave_race` event goes through the exact same single-writer `applyEvent` path as every other `RoomEvent` — no new goroutine, no new channel.
- `checkRaceFinished` now reads two collections instead of one (`participants` plus the departed set) — still entirely inside the single-writer goroutine, no new synchronization needed.

## Data

```go
// internal/race: new sentinel error
var ErrNotParticipant = errors.New("race: caller is not a participant")

// internal/room/events.go: existing type renamed, no behavior change —
// just freeing the ParticipantLeft name for the new event below.
// ParticipantLeft -> ParticipantEvicted

// internal/room/events.go: new RoomEvent, taking over the ParticipantLeft
// name. No DisconnectedAt guard, unlike ParticipantEvicted — a still-
// connected participant is exactly who's expected to send this.
type ParticipantLeft struct {
    UserID string
}

func (ParticipantLeft) isRoomEvent() {}
```

- `websocket/protocol.md`'s `ClientMessage` gains a third valid `Type`: `"leave_race"`.
- `RoomActor` gains (exact shape TBD at `start`): a `totalParticipants int` counter, and a place to keep departed-but-not-finished participants queryable at finish time instead of discarding them outright.

## Notes

- Source of this spec: explicit user request, not `context/project-overview.md`.
- Depends on `race-completion/finish-race.md` (done) — this updates that feature's rank logic rather than replacing it; loading this spec should also mean re-reading that one.
- Depends on `reconnection/grace-period.md` (done) for the `evicted`/`IsEvicted` machinery this reuses for intentional quitters too.
- Worth sequencing before `frontend-realtime/reconnect-ui.md` if that feature wants to expose a "leave race" button, though nothing in `phase-2-plan.md` strictly orders them today.
