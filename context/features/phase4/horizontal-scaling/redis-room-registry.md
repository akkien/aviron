# Redis Room Registry (Ownership Only)

## Overview

**Recreated, not redesigned.** This spec's file was deleted along with
the rest of the previous Phase 4 set, but `internal/roomlocator` — the
package it describes — was never touched and still exists exactly as
built. Nothing about the registry's own design changes under the WS
Gateway revision (`phase-4-plan.md`, `docs/knowledge-summary.md`'s
"Revision: adopting a WS Gateway tier"); what changes is *who consults
it and for what* — see "Consultation pattern, updated" below. This file
exists so the spec set is complete and self-contained again, and so the
updated consultation pattern is written down somewhere other than a
diagram caption.

A room has exactly one instance that ever owns it, decided by
construction: whichever `race-service` instance handles `POST /races`
becomes the owner. Redis's only job is recording that fact durably so
every other process — `ws-gateway` for REST routing, any `race-service`
instance checking whether it's safe to spawn a `RoomActor` — can read it
without hardcoding server topology anywhere. No hash formula, consistent-
hashing ring, or rebalancing scheme ever decides ownership, so there's no
"ring reshuffle silently orphans a live room" risk to design around.

## Current state (confirmed by reading the code)

`internal/roomlocator/locator.go` exists, is fully implemented, and needs
no changes for this revision:

```go
func NewLocator(client *redis.Client, instanceID string) *Locator
func (l *Locator) Claim(ctx context.Context, raceID string) (bool, error)
func (l *Locator) Refresh(ctx context.Context, raceID string) error
func (l *Locator) Release(ctx context.Context, raceID string) error
func (l *Locator) Owner(ctx context.Context, raceID string) (string, bool, error)
func (l *Locator) SubscribeRoomEvents(ctx context.Context) (<-chan RoomEvent, error)
```

- `Claim`: `SET room:<id> instance:<id> NX EX 60` — first writer wins,
  60s TTL. Publishes a `room:events` `created` notification on success.
- `Refresh`: `EXPIRE room:<id> 60` — the owning instance's periodic
  heartbeat, keeping the claim alive for as long as the room actually
  runs, well past the initial 60s.
- `Release`: `DEL room:<id>` plus a `room:events` `removed` notification —
  called once the room finishes or is cancelled.
- `Owner`: a plain `GET room:<id>`, `instance:` prefix stripped.
- `SubscribeRoomEvents`: subscribes to the single shared `room:events`
  pub/sub channel, decodes `{type, race_id, instance_id}` payloads,
  skips malformed ones rather than treating them as fatal.

The only stale thing in the existing code is its own comments — several
reference `race-router.md` by name as the consumer of `Owner`/
`SubscribeRoomEvents`. That file no longer exists; `ws-gateway.md` (this
phase) is the real consumer now. Worth a small comment fix when
`ws-gateway` lands, not a functional change — noted here so it isn't
mistaken for a design gap.

## Consultation pattern, updated

This is the one real change this spec makes relative to the previous
(also superseded) version — not to the registry itself, but to which
processes call it and why:

| Consumer | Calls | Why |
| --- | --- | --- |
| `ws-gateway` | `Owner` (cached, TTL + pub/sub-warmed, same shape `race-router.md` already designed) | REST routing only: a room-scoped `/races/{id}/...` request needs to know which `race-service` instance to proxy to |
| `race-service` (the owning instance) | `Claim`, `Refresh`, `Release` | Ownership bookkeeping — exactly one instance runs a given room's `RoomActor`, and that fact is durably visible in case it crashes and a client's eviction-check needs to notice the claim has expired |
| `race-service` (any instance, spawning a room) | `Claim` at `POST /races` time | Deciding-by-construction: whichever instance's handler processes the create call claims the room right there, before any client ever connects |

**What no longer consults it: WebSocket traffic, at all.** Under the
previous `race-router` design, `Owner` was on the hot path for *every*
room-scoped request, REST and WS alike — a `GET /ws?race_id=...` upgrade
needed the lookup to know which instance to proxy the raw connection to.
Under this revision, `ws-gateway` never proxies the WebSocket anywhere;
it terminates it locally and relays decoded messages onto
`room.{race_id}.*` (`room-message-bus.md`, on NATS — see that spec's
"Correction (this pass)" note), whose own subject-name addressing means
the owning `race-service` instance receives those messages by virtue of
being the only subscriber — no `Owner` call involved. This is a genuine
simplification of the registry's role, not
just a side effect: the registry now answers exactly one question
("which instance handles this REST call"), not two.

## Requirements

Unchanged from the original build — restated here for completeness, not
because anything needs re-implementing:

- `INSTANCE_ID` (`internal/config.Config`) identifies each `race-service`
  process; `NewLocator` is constructed with it once at startup.
- `room.Registry.Spawn` calls `Claim` before starting a `RoomActor`, and
  runs a periodic (well under 60s — confirm the exact interval against
  the existing code, already tuned) `Refresh` heartbeat for as long as
  the room is active.
- `room.Registry`'s cleanup path (room finished, cancelled, or otherwise
  torn down) calls `Release`.

## Testing

Already covered by the existing test suite (`internal/roomlocator/
locator_test.go`, `internal/room/registry_locator_test.go`) — no new
tests needed for the registry itself under this revision. New tests
belong to whichever spec actually changes behavior:
`room-service-adapter.md` (Room Service side) and `ws-gateway.md`
(Gateway side).

## Notes

- **Single Redis instance, still a deliberate, disclosed simplification.**
  A real deployment of this design should run Redis as a Cluster with
  per-shard replication — the registry becoming unavailable stops new
  joins/reconnects (and now, eviction checks — see `ws-gateway.md`) from
  resolving correctly, and a single node has no automatic failover. This
  project keeps a single instance for local-dev simplicity, the same
  category of accepted single-point-of-failure risk already carried for
  the one, non-HA Postgres instance.
- **Correction (this pass): `room-message-bus.md`'s `internal/roomrelay`
  package no longer shares this Redis instance at all.** It was
  originally designed as a separate Redis pub/sub usage (channels, not
  registry keys) on the same connection; it now runs on NATS instead —
  see that spec's own "Correction (this pass)" note for why. This Redis
  instance's job stays exactly what's described in this file: registry
  keys and `room:events` cache-invalidation only, nothing message-bus
  related landing on it anymore.
