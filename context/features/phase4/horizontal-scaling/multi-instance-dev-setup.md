# Multi-Instance Local Dev Setup & Verification

## Overview

`redis-room-registry.md` and `cross-instance-relay.md` build the
mechanism; this spec is where it gets proven against a real second
process, deliberately **before** `context/features/phase5/` (Kubernetes)
enters the picture at all — it's a hard prerequisite for that phase, not
just a convenient ordering (see `phase5/phase-5-plan.md`'s "Hard
dependency" section). Per `context/project-overview.md` §7's own warning
("doing them earlier tends to turn into infrastructure overhead rather
than Go practice"), debugging a Redis pub/sub bug and Kubernetes
networking at the same time would make it much harder to tell which layer
a failure came from — this spec exists specifically to rule out the
Redis-relay layer as a bug source before Kubernetes adds its own.

No Kubernetes, no load balancer even — this spec's whole point is the
smallest possible setup that still has two genuinely separate backend
processes.

## Requirements

### Infra

- `redis:7-alpine` added to `docker-compose.yml` (likely already present
  from `redis-room-registry.md` — confirm at `start`, don't duplicate).
- No new Postgres instance — both backend processes share the one existing
  `docker-compose.yml` Postgres, exactly like real horizontally-scaled
  instances would share one database.

### Running two instances

- Document (in this spec, or a new `docs/multi-instance-dev.md` if this
  project's convention of putting durable how-tos in `docs/` — see
  `docs/concurrency.md` — extends here; confirm at `start`) the exact
  commands to run two `cmd/server` processes locally against the same
  Postgres/Redis:

  ```bash
  INSTANCE_ID=a PORT=8080 go run ./cmd/server
  INSTANCE_ID=b PORT=8081 go run ./cmd/server
  ```

- No reverse proxy or load balancer is required for this spec — a human
  tester (or a script) picks which instance to hit by port number
  directly, which is precisely what makes this setup good for isolating
  relay bugs: the tester controls routing explicitly instead of trusting a
  load balancer's own routing decision.

### Verification plan (the actual acceptance test for the previous two specs)

Write this as a repeatable script (`load/multi-instance-check.sh` or
similar — reuse the `load/` directory's existing shape rather than
inventing a new top-level location) rather than a purely manual checklist,
so it can be re-run after any future change touches the relay:

1. Register/login a user against instance A (`:8080`).
2. `POST /races` against instance A → the room is created and owned by
   instance A (confirm via a direct `redis-cli GET room:<id>` — a real,
   inspectable assertion, not just "it seemed to work").
3. Register/login a second user against instance **B** (`:8081`).
4. `POST /races/{id}/join` against instance B — this is a REST call that
   doesn't touch `room.Registry` at all today (confirmed in
   `cross-instance-relay.md`'s "current state" section: only `Create` and
   `Start` touch it), so this step should already work with zero relay
   code — worth confirming explicitly, since if it doesn't, the bug is
   elsewhere, not in the relay.
5. Open a WebSocket to instance **B** (`GET /ws?race_id=...`) using user
   2's session token — this is the real cross-instance test:
   `cross-instance-relay.md`'s relay path must find the room is owned by
   instance A and attach correctly.
6. `POST /races/{id}/start` against instance **A** (the owner) — confirm
   both the instance-B WebSocket (via the relay) and a WebSocket opened
   directly against instance A both receive `race_started`.
7. Send `telemetry` messages from the instance-B WebSocket connection —
   confirm (via a WebSocket opened against instance A) that the other
   participant's `race_state` broadcasts reflect user 2's progress. This
   proves the inbound relay path (`room:<raceID>:in`).
8. Race to completion — confirm both connections receive
   `race_finished` with correct, matching results.
9. Repeat step 5-8 with the roles of A/B reversed for a second race, to
   rule out an accidental "only works in one relay direction" bug.
10. Kill instance A's process mid-race (simulating the owner dying — the
    accepted gap from `cross-instance-relay.md`'s open question #3) and
    confirm instance B's orphaned connection eventually fails the way that
    spec predicted (client-side reconnect-with-backoff exhausts and
    surfaces as evicted/disconnected, not a hang) — this is the one step
    in this plan that's about confirming a *documented gap* behaves as
    expected, not about proving something works.

### What "done" means for this spec

Every step above passing, run at least twice back-to-back against a fresh
`docker-compose down -v && docker-compose up -d` to rule out state leaking
between runs — this project's existing verification bar for concurrency-
sensitive features (`internal/room`/`internal/ws` re-run 3-5x per feature)
extends naturally to this spec's actual infra-level test.

## Notes

- This is the spec where `horizontal-scaling/`'s design gets to fail
  loudly and cheaply, on a laptop, instead of first inside Kubernetes where
  a failure could be networking, DNS, or the relay design, tangled
  together. Do not skip ahead to Phase 5's
  `k8s-race-service-deploy.md` (`replicas: 2`) until every step above
  passes cleanly.
- If any step reveals a real design gap in `cross-instance-relay.md` (not
  just a bug in its implementation), fix that spec's design and revise its
  file before continuing — same "grounding must reflect reality" principle
  every prior phase plan in this project already follows, not unique to
  this spec.
