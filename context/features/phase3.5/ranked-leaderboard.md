# Ranked Leaderboard (`GET /leaderboard`)

## Overview

`GET /leaderboard/me` (`user-stats.md`, Phase 2.5) already answers "what
are my own stats" from `leaderboard_alltime`. This spec adds the other
half: "who's actually winning" — a ranked, multi-user leaderboard with two
windows, `alltime` and `weekly`, per the leftover item flagged repeatedly
in `context/current-feature.md`'s history and finally scheduled in
`phase-3.5-plan.md`.

Extends the existing `internal/leaderboard` package — same domain, no new
package, following this project's Handler/Service/Repository convention
(`coding-standards.md`) exactly as `Me` already does.

## Current schema (confirmed by reading the code, not assumed)

- `leaderboard_alltime` (`user_id`, `best_2000m_ms`, `total_races`,
  `total_distance_m`, `total_wins`, `total_pace_watt_sum`, `updated_at`) —
  a running lifetime aggregate, updated only inside `FinishRace`'s
  transaction (`internal/postgres/race_repository.go`). A user with no
  finished race has no row at all.
- `race_participants` (`race_id`, `user_id`, `finish_rank`,
  `finish_time_ms`, `avg_pace_watt`, `disconnected_count`, `joined_at`) —
  per-race results, `finish_rank`/`finish_time_ms`/`avg_pace_watt` all set
  by the same `FinishRace` transaction, `NULL` until then.
- `races.status` (`pending|active|finished|cancelled`), `races.ended_at`
  (set alongside `status = 'finished'` in the same transaction).

Neither table needs a schema change for this spec — `alltime` reads
directly from `leaderboard_alltime` (already exactly the right shape);
`weekly` is computed live from `race_participants` joined to `races`,
filtered by `ended_at`. No new migration for tables/columns, only a new
index (see Data).

## API

`GET /leaderboard?window=alltime|weekly&limit=N` — `requireAuth`-wrapped,
matching every other `/leaderboard*` and `/races*` route (no existing
precedent in this codebase for an unauthenticated data endpoint besides
`/healthz`/`/metrics`, both operator endpoints, not user-facing data).

- `window` — required, exactly `alltime` or `weekly`; anything else is a
  `400` with a field-keyed error (`{"errors": {"window": "must be
  alltime or weekly"}}`), matching this project's existing validation
  error shape (e.g. `auth.Register`'s field errors).
- `limit` — optional, default 20, clamped to a max (e.g. 100) rather than
  rejected if a caller asks for more — a capped response is safer than a
  500 from an unbounded `LIMIT`-less query and doesn't need its own error
  path. Confirm the exact default/max at `start`; not a load-bearing
  number, just needs to exist so a client can't request the entire table.
- Response: an ordered list, each entry `{"rank": 1, "user_id": "...",
  "display_name": "...", "races": N, "wins": N, "avg_wpm": 42.5}` — reuses
  the same field names/shape `LeaderboardMeResponse` already established
  (`races_joined`→`races`, `races_won`→`wins`, `avg_wpm` unchanged) rather
  than inventing new naming, since it's the same underlying metric.

## Open design questions for `start`

Flagged rather than silently decided, since both are real judgment calls
with no existing precedent in this codebase to defer to:

1. **Primary sort key.** Two defensible options: rank by `wins` (a
   "leaderboard" is fundamentally about who's won the most races,
   `avg_wpm` as tiebreaker) or rank by `avg_wpm` (a "who types fastest"
   framing, `wins` as tiebreaker). Leaning toward **wins primary** — it's
   the more conventional "leaderboard" framing and avoids rewarding a
   single unusually-fast race on a low sample size — but this is a real
   product decision, not a technical one, confirm before implementing the
   `ORDER BY`.
2. **"Weekly" as a rolling 7-day window vs. a calendar week.** A rolling
   `races.ended_at >= now() - interval '7 days'` needs no timezone
   handling and no "what day does the week start on" decision; a calendar
   week is more intuitive to read ("this week's leaderboard") but adds
   timezone-boundary complexity for a side project with no stated
   timezone requirement anywhere else in this codebase. Leaning toward
   **rolling 7 days** for simplicity — confirm at `start`.

## Design

### Repository

```go
// internal/leaderboard/repository.go
type Window string

const (
    WindowAllTime Window = "alltime"
    WindowWeekly  Window = "weekly"
)

type Entry struct {
    UserID      string
    DisplayName string
    Races       int
    Wins        int
    AvgWPM      float64
}

type LeaderboardRepository interface {
    GetUserStats(ctx context.Context, userID string) (Stats, error) // unchanged
    GetTop(ctx context.Context, window Window, limit int) ([]Entry, error)
}
```

- `AvgWPM` is computed in SQL for this method (unlike `GetMyStats`, which
  divides in the service layer) since it's needed for `ORDER BY`
  regardless — computing it once in the query and reusing it for both
  sorting and the response avoids a second pass in Go.

### Postgres queries

All-time, straight off the existing aggregate table:

```sql
SELECT u.id, u.display_name, la.total_races, la.total_wins,
       la.total_pace_watt_sum / la.total_races AS avg_wpm
FROM leaderboard_alltime la
JOIN users u ON u.id = la.user_id
WHERE la.total_races > 0
ORDER BY la.total_wins DESC, avg_wpm DESC
LIMIT $1
```

(`total_races > 0` is already implied — `leaderboard_alltime` only ever
gets a row via `FinishRace`'s `INSERT` — kept explicit for readability,
and to guard the division regardless.)

Weekly, computed live (no aggregate table to read from):

```sql
SELECT u.id, u.display_name, count(*) AS races,
       count(*) FILTER (WHERE rp.finish_rank = 1) AS wins,
       avg(rp.avg_pace_watt) AS avg_wpm
FROM race_participants rp
JOIN races r ON r.id = rp.race_id
JOIN users u ON u.id = rp.user_id
WHERE r.status = 'finished' AND r.ended_at >= now() - interval '7 days'
GROUP BY u.id, u.display_name
ORDER BY wins DESC, avg_wpm DESC
LIMIT $1
```

- `r.status = 'finished'` excludes `cancelled` races a user may have
  joined — those never got a `finish_rank`, and a cancelled race
  shouldn't count toward anyone's weekly form.
- No existing index supports `races.ended_at` range filtering — see Data.

### Service

`LeaderboardService.GetTop(ctx, windowParam string, limit int)
(LeaderboardResponse, error)` — parses/validates `windowParam` into
`Window` (returning the field-keyed validation error on mismatch, same
pattern `auth.AuthService.Register` already uses), clamps `limit`, calls
`repo.GetTop`, and assigns `rank` by position in the already-sorted slice
(`rank: i+1` — no dense-ranking/tie-splitting logic, consistent with how
`checkRaceFinished`'s own rank assignment elsewhere in this codebase
doesn't do anything more elaborate than a stable ordering either).

### Handler

`LeaderboardHandler.Top(w, r)` — parses `window`/`limit` query params,
delegates to the service, `400` on validation failure, `200` with the
ordered list otherwise. Registered as `GET /leaderboard` in
`internal/httpserver/route.go`, right alongside the existing
`GET /leaderboard/me` — no pattern conflict (Go 1.22 `ServeMux` treats
`/leaderboard` and `/leaderboard/me` as distinct literal patterns).

## Data

New migration `000005_leaderboard_query_index` — per
`project-overview.md` §3's own stated goal ("Index according to real
query patterns... not indiscriminately"), the weekly query's
`WHERE r.status = 'finished' AND r.ended_at >= ...` has nothing to use
today:

```sql
CREATE INDEX idx_races_status_ended_at ON races (status, ended_at)
  WHERE status = 'finished';
```

A partial index (only `finished` rows) rather than a plain
`(status, ended_at)` index — every row this query ever needs to scan is
`status = 'finished'`, so indexing `pending`/`active`/`cancelled` rows
too would only add write overhead for zero read benefit.

## Testing

- `internal/leaderboard`'s existing fake-repository convention
  (`helpers_test.go`) extended with a `GetTop` fake — service tests for:
  invalid `window` value, default/clamped `limit`, correct `rank`
  assignment over an already-sorted fake result.
- Handler tests: `200` with a valid `window`, `400` for missing/invalid
  `window`, `401` with no auth — mirrors `Me`'s existing test shape.
- No new `internal/postgres` test file, per this project's established
  convention (`coding-standards.md`: repository correctness is proven
  through the service-layer fake, not direct DB tests) — but worth one
  manual verification against real Postgres (per this project's own
  precedent for schema/query-shape changes, e.g. the base58 race-ID
  migration's live verification) confirming the weekly window actually
  excludes a race older than 7 days and a cancelled race.

## Notes

- Deliberately no frontend work in this spec's core scope — the user's
  own request was "create leaderboard with Postgres first," read as
  backend-first. A minimal list view (reusing `StatCards`' existing
  card-shell styling, a simple ranked table) is a small, natural follow-up
  once this endpoint exists, but isn't assumed here.
- This is the spec a future ClickHouse migration would target, if one is
  ever picked up (`phase4/phase-4-plan.md`'s "Explicitly out of scope" —
  ClickHouse dropped, not forgotten) — the two queries above are exactly
  the "top N over a window" access pattern that would motivate it. Not
  addressed here; this spec's own Overview explains why Postgres comes
  first.
