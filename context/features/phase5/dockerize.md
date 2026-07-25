# Dockerize the Backend Services

## Overview

Confirmed by `find . -iname "Dockerfile*"` across the whole repo before
writing this plan: **no Dockerfile exists anywhere in this project today.**
Every prior phase ran the backend via `go run`/`make start` directly
against a `docker-compose`-hosted Postgres — the backend itself was never
containerized, because nothing needed it to be until Kubernetes. This spec
is the prerequisite `k8s-core-infra.md`/`k8s-race-service-deploy.md`
cannot start without: `kind load docker-image` needs an image to load.

By the time this spec is picked up (first of this phase, which itself only
starts once `context/features/phase4/` is done — see `phase-5-plan.md`'s
"Hard dependency" section), this project should have two backend
binaries, not one — confirm both exist at `start` and scope this spec to
them:

- `cmd/server` — the Race Service (REST + WebSocket), existed before
  Phase 4.
- `cmd/consumer` — the Kafka consumer (Phase 4's
  `event-pipeline/kafka-consumer-postgres-sink.md`).

No `cmd/analytics` — Phase 4 dropped the internal gRPC service entirely
(no product need; see `phase4/phase-4-plan.md`'s "Explicitly out of
scope"), so there's nothing here to containerize for it.

## Design

### One parameterized `Dockerfile`, not three

A single multi-stage `Dockerfile` at the repo root, parameterized by a
build arg for which `cmd/` package to build, rather than three
near-identical files that would drift out of sync with each other over
time:

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
ARG SERVICE=server
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

- Confirm the exact `golang:` base tag at `start` against this module's
  real `go.mod` (`go 1.26.4`, confirmed by reading `backend/go.mod`
  directly) — the base image tag must not silently drift below what the
  module declares.
- `distroless/static-debian12` for the final stage: `CGO_ENABLED=0` and
  this project's dependencies (`pgx`, `redis-go`, `kafka-go`) are all
  pure-Go, so a static binary needs no C runtime — smaller image,
  smaller attack surface, and no shell for anything to exploit if it ever
  mattered for a side project (it doesn't much, but it's the same cost as
  `alpine` for the final stage and strictly better).
- `backend/migrations/` is **not** copied into any of these images —
  `db.Migrate` (`internal/db/migrate.go`) reads from `file://migrations`
  relative to the process's working directory, which only `cmd/server`
  actually calls (confirmed by reading `internal/app.go`) — confirm at
  `start` whether that path assumption still holds once running inside a
  container with a different working directory, and whether migrations
  should instead run as a separate `kind load`-able init step/Job rather
  than being baked into the `cmd/server` image at all (the more common
  Kubernetes pattern — a `Job` or `initContainer` running migrations
  before the Deployment's pods start serving, so a migration failure
  blocks rollout instead of shipping a pod that starts and then fails
  every request). This decision belongs to whichever of
  `k8s-core-infra.md`/`k8s-race-service-deploy.md` ends up owning the
  migration-running step — flagged here since it affects whether this
  Dockerfile needs to `COPY` `migrations/` at all.

### Build script

- `make docker-build` (new `Makefile` target, or a small `deploy/
  build-images.sh` — confirm at `start` which fits this project's existing
  `Makefile`-first convention better) builds each real binary's image:
  `docker build --build-arg SERVICE=server -t aviron/race-service:dev .`,
  repeated per binary that actually exists.
- `make kind-load` (or folded into the same script): `kind load
  docker-image aviron/race-service:dev --name <cluster-name>`, per binary
  — the local-Kubernetes equivalent of `docker-compose up`, and exactly
  what §7 calls out as sufficient ("no need for a complex CI/CD setup...
  `kind load docker-image`... is enough").

### `.dockerignore`

New `backend/.dockerignore` (or repo-root, matching whichever `COPY`
context the final `Dockerfile` design at `start` settles on) excluding at
minimum `frontend/`, `load/`, `.git/`, `docs/`, and any local `.env` —
nothing outside `backend/` is ever needed inside these images, and an
`.env` accidentally baked into an image would leak local dev secrets into
a layer, however low-stakes those particular secrets are for this project.

## Verification

- `docker build` succeeds for every image, and `docker run` (against the
  already-running `docker-compose` Postgres/Redis/Kafka, via
  `--network`/host networking or explicit `-e DATABASE_URL=...` pointing
  at the host) actually starts and serves — this is the real acceptance
  test, not just "the build didn't error." A binary that builds cleanly
  but panics on startup inside a distroless image (a missing CA cert
  bundle for TLS outbound calls, a missing timezone database, etc. — real,
  common distroless gotchas) would otherwise not be caught until
  `k8s-core-infra.md`.
- Image size sanity check (`docker images`) — not a hard requirement, but
  worth confirming the distroless choice actually produced something
  meaningfully smaller than a naive single-stage build, as a basic sanity
  check that the multi-stage `Dockerfile` is doing what it's supposed to.

## Notes

- This spec produces no Kubernetes manifests and changes no application
  code (beyond, possibly, the migrations-path question flagged above,
  which may turn out to belong to a different spec entirely once resolved
  at `start`) — it's infrastructure-only, kept separate from
  `k8s-core-infra.md` so a Dockerfile problem and a Kubernetes manifest
  problem are never debugged at the same time.
