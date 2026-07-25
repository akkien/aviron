# Multi-Instance Local Dev Setup & Verification

## Overview

`redis-room-registry.md` and `race-router.md` build the mechanism (`race-
router.md` supersedes the original `cross-instance-relay.md` design — see
that file's own "Superseded" section); this spec is where the combination
gets proven against real separate processes, deliberately **before**
`context/features/phase5/` (Kubernetes) enters the picture at all — it's a
hard prerequisite for that phase, not just a convenient ordering (see
`phase5/phase-5-plan.md`'s "Hard dependency" section). Per
`context/project-overview.md` §7's own warning ("doing them earlier tends
to turn into infrastructure overhead rather than Go practice"), debugging
a routing bug and Kubernetes networking at the same time would make it
much harder to tell which layer a failure came from — this spec exists
specifically to rule out the registry/router layer as a bug source before
Kubernetes adds its own.

No Kubernetes — this spec's whole point is the smallest possible setup
that still has two genuinely separate backend processes plus the router
in front of them, unlike the original design this supersedes, which
needed no separate router process at all (a human/script picked the port
directly). `race-router` itself **is** the load balancer this spec now
needs — deliberately still not a real one (no TLS, no health checks),
matching this project's convention of building the smallest thing that
actually proves the design.

## Requirements

### Infra

- `redis:7-alpine` added to `docker-compose.yml` (likely already present
  from `redis-room-registry.md` — confirm at `start`, don't duplicate).
- No new Postgres instance — both backend processes share the one existing
  `docker-compose.yml` Postgres, exactly like real horizontally-scaled
  instances would share one database.

### Running two instances, plus the router

- Document (in this spec, or a new `docs/multi-instance-dev.md` if this
  project's convention of putting durable how-tos in `docs/` — see
  `docs/concurrency.md` — extends here; confirm at `start`) the exact
  commands to run two `cmd/server` processes and one `cmd/race-router`
  locally against the same Postgres/Redis:

  ```bash
  INSTANCE_ID=a PORT=8080 go run ./cmd/server
  INSTANCE_ID=b PORT=8081 go run ./cmd/server
  RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081 \
    PORT=9090 go run ./cmd/race-router
  ```

- Every test in the verification plan below connects through the router's
  port (`:9090`), not directly to `:8080`/`:8081` — that's the entire
  point of this spec's acceptance test: proving a client that only ever
  talks to one address (the router) reaches the correct room regardless
  of which of the two backend processes actually owns it. Direct
  connections to `:8080`/`:8081` are still useful for isolating whether a
  bug is in `race-router` or in `race-service` itself (bypass the router
  to rule it out), but aren't the primary path being verified.

### Verification plan (the actual acceptance test for the previous two specs)

Write this as a repeatable script (`load/multi-instance-check.sh` or
similar — reuse the `load/` directory's existing shape rather than
inventing a new top-level location) rather than a purely manual checklist,
so it can be re-run after any future change touches the relay:

1. Register/login a user against the router (`:9090`) → confirm it landed
   on one of the two instances (either is fine, round-robin has no
   correctness requirement here).
2. `POST /races` against the router → the room is created and owned by
   whichever instance actually received it (confirm both which instance
   via that instance's own logs, and via a direct `redis-cli GET
   room:<id>` — a real, inspectable assertion, not just "it seemed to
   work"). Call this the **owning instance** for the rest of this plan,
   regardless of whether it turns out to be A or B.
3. Register/login a second user against the router.
4. `POST /races/{id}/join` against the router — the router must route
   this to the owning instance (confirmed via that instance's logs, not
   the other one's) even though the request came from a fresh connection
   that has no reason to land there on its own.
5. Open a WebSocket to `GET /ws?race_id=...` **through the router**, using
   user 2's session token — this is the real cross-instance test:
   `race-router.md`'s `Director` must resolve `race_id` to the owning
   instance and proxy the WS upgrade there, transparently. Confirm via the
   owning instance's own logs that it — not the other instance — accepted
   the connection and ran its normal local `IsEvicted` check.
6. `POST /races/{id}/start` against the router — confirm the WebSocket
   opened in step 5 receives `race_started`, exactly as it would talking
   directly to the owning instance.
7. Send `telemetry` messages from that WebSocket connection — confirm (via
   a second WebSocket, also opened through the router) that the other
   participant's `race_state` broadcasts reflect user 2's progress. Since
   both connections are now proxied straight to the same owning instance,
   this is really confirming the router got the routing decision right
   twice in a row (once per connection), not testing any relay/forwarding
   logic inside `race-service` itself — there isn't any left to test.
8. Race to completion — confirm both connections receive `race_finished`
   with correct, matching results.
9. Repeat steps 2-8 several times to get a mix of "owning instance is A"
   and "owning instance is B" outcomes from the router's round robin,
   ruling out an accidental "only routes correctly to one specific
   instance" bug in `Director`.
10. Kill the owning instance's process mid-race (simulating the owner
    dying — the same accepted gap `cross-instance-relay.md` originally
    flagged as its open question #3, carried forward unchanged by
    `race-router.md`'s own Notes) and confirm the other connection's
    reconnect attempt (through the router) eventually fails the way that
    gap predicts: the router's cache TTL expires, `Owner` starts missing
    too, and the client's reconnect-with-backoff exhausts and surfaces as
    evicted/disconnected, not a hang. This is the one step in this plan
    about confirming a *documented gap* behaves as expected, not about
    proving something works.

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
- If any step reveals a real design gap in `race-router.md` (not just a
  bug in its implementation), fix that spec's design and revise its file
  before continuing — same "grounding must reflect reality" principle
  every prior phase plan in this project already follows, not unique to
  this spec.
