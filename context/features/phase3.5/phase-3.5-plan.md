# Phase 3.5 — Ranked Leaderboard (Postgres)

## Overview

Closes out the last of the three "leftover items" surfaced during the
audit documented in `context/current-feature.md`'s "Idempotent Join /
Session Token Recovery" history entry: a full ranked/windowed
`GET /leaderboard?window=alltime|weekly` was flagged there as "still
unclaimed by any phase, not picked up here" — this phase is where it gets
picked up.

## Why its own phase, and why `3.5` specifically

Raised directly by the user before this plan was written, and worth
recording the reasoning rather than just the conclusion:

- **Not Phase 4.** `context/features/phase4/phase-4-plan.md` is Redis
  horizontal-scaling and the Kafka event pipeline — both non-functional,
  infrastructure concerns that don't change what a user can do, only how
  the system scales. A ranked leaderboard is the opposite: a new,
  user-visible REST endpoint with real product value. Folding it into
  Phase 4 would mean that plan's own "one architectural decision at a
  time" discipline (its own "A note on scope discipline" section)
  immediately applies to a phase mixing a functional feature in with
  infrastructure work — exactly what that discipline exists to prevent.
- **Not Phase 2.7.** `2.7` was floated and rejected: Phase 2.5 and 2.6
  were both inserted *before* Phase 3 was built, back when Phase 3 was
  still upcoming work — `2.x` numbering meant "additional work before
  moving on to Phase 3." Phase 3 (`context/features/phase3/`) is now
  fully shipped. Numbering this `2.7` would misrepresent it as slotting
  in before already-completed work, when it actually belongs *after* it —
  chronologically, this phase sits between Phase 3 (done) and Phase 4
  (spec'd, not started).
- **`3.5` matches this project's own established convention** for exactly
  this situation: a self-contained functional feature that doesn't fit
  the theme of the phase before or after it, inserted at its real
  chronological position — the same shape `phase2.5` (UI revamp + user
  stats, inserted between Phase 2 and Phase 3) already used.

## Why Postgres, with ClickHouse deliberately deferred

Discussed and agreed before this plan was written (see conversation
leading up to this phase, not repeated in full here): build the ranked
query against Postgres first, prove the feature and its access patterns
for real, and treat a ClickHouse migration as a separate, later,
explicitly-scoped piece of work — not something to design blind before
the query patterns it would serve even exist. At this project's actual
scale, a weekly/all-time ranked query is trivial for Postgres; ClickHouse
only pays for itself once there's a real reason to reach for it, and this
phase is what would eventually generate that reason (or not).

`context/features/phase4/phase-4-plan.md`'s own "Explicitly out of scope"
section already documents that ClickHouse is dropped from Phase 4 — this
phase doesn't change that decision, it's what a future ClickHouse spec (if
one is ever written) would migrate.

## Spec

- `ranked-leaderboard.md` — `GET /leaderboard?window=alltime|weekly&limit=N`,
  extending the existing `internal/leaderboard` package (already houses
  `GET /leaderboard/me`) rather than a new domain package.

## Dependency

None on Phase 4 or Phase 5 — this is a plain REST/Postgres feature, the
same shape as every other `internal/leaderboard`/`internal/race` endpoint
already shipped. It can be built immediately, before Phase 4 starts.
