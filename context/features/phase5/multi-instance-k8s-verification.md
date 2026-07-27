# Multi-Instance Verification on Kubernetes

## Overview

The real acceptance test for this entire phase — everything in
`k8s-core-infra.md`, `graceful-shutdown.md`, `k8s-race-service-deploy.md`,
`k8s-ws-gateway-deploy.md`, and `k8s-consumer-deploy.md`, together, under
`kubectl`, not five specs each individually "looking done." Mirrors
`load/multi-instance-check.md`'s own framing of its job relative to
Phase 4's individual specs: "don't treat either side's unit tests [or,
here, either spec's own verification section] as sufficient proof the
whole thing actually works together."

Two things this phase specifically needs proven that no earlier spec's
own verification already covers in combination:

1. **Cross-gateway room consistency survives being orchestrated by
   Kubernetes**, not just two hand-run `go run` processes.
2. **A rolling update behaves like `graceful-shutdown.md`'s "let
   in-progress races finish naturally" decision**, not like
   `context/feature-history.md`'s documented ungraceful-crash gap (a
   silent hang until Redis's ~60s ownership TTL lapses).

## Topology

```text
race-service-0, race-service-1   -- StatefulSet, headless Service, stable DNS
ws-gateway (2 pods)                -- Deployment, behind one ClusterIP Service + Ingress
postgres, redis, nats, kafka       -- k8s-core-infra.md
consumer (1 pod)                    -- k8s-consumer-deploy.md
```

Unlike `load/multi-instance-check.md`'s script, which deliberately talks
to each gateway on its own distinct port to force a specific gateway
choice per participant, a real `Ingress`/`Service` load-balances
requests across both `ws-gateway` pods — this verification needs a way to
pin one participant to each pod anyway (the property that actually
exercises the bus, per `ws-gateway.md`'s own reasoning), so:

- Use `kubectl port-forward svc/ws-gateway-0` — not possible for a plain
  `Deployment`'s pods by name the way a `StatefulSet`'s are; instead
  `kubectl port-forward pod/<ws-gateway-pod-name-1>` and
  `pod/<ws-gateway-pod-name-2>` directly (get the two pod names via
  `kubectl get pods -l app=ws-gateway` first), each forwarded to a
  different local port, mirroring `load/multi-instance-check.md`'s own
  two-distinct-endpoints setup exactly, just against pods instead of
  local processes.

## What it does

Port largely 1:1 from `load/multi-instance-check.md`'s own script,
against the cluster instead of `docker compose`/local binaries:

1. Apply every manifest from `k8s-core-infra.md` through
   `k8s-consumer-deploy.md` to a fresh `kind` cluster; wait for every pod
   `Running`/`Ready`.
2. `kubectl port-forward` to each of the two `ws-gateway` pods on
   distinct local ports.
3. Register two users, create a race, join the second user — through the
   two different forwarded `ws-gateway` pods, exactly like
   `load/multi-instance-check.md`'s step 2.
4. Confirm ownership two ways: `kubectl exec` into the Redis pod
   (`redis-cli GET room:<id>`), and `kubectl logs` on whichever
   `race-service-0`/`race-service-1` pod owns it, grepping for the race
   ID the same way the existing script does (via the `RequestLog`
   middleware's access-log line, per that script's own "Corrections"
   section — not the room actor's own error-path-only logging, a mistake
   already made and fixed once).
5. Confirm the bus itself carried traffic: `kubectl logs` on the owning
   `race-service` pod for `roombus: published`, and on each `ws-gateway`
   pod for `wsgateway: received`, both tagged with the race ID — the same
   assertion `load/multi-instance-check.md` added specifically because a
   silently-dropped relay message could otherwise pass a small enough
   test race unnoticed.
6. Open both players' WebSocket connections through their respective
   forwarded pod, start the race, stream telemetry, confirm both receive
   matching `race_finished` results.
7. Repeat 3-6 several times (`REPEAT_RUNS`, same knob and default as
   `load/multi-instance-check.md`) until both `race-service` pods have
   owned at least one race.
8. **New to this spec, not in `load/multi-instance-check.md`**: start a
   race, then `kubectl rollout restart statefulset/race-service` (or
   `deployment/ws-gateway`, run as two separate passes — each exercises a
   different part of `graceful-shutdown.md`'s design) mid-race. Confirm
   the outcome matches `graceful-shutdown.md`'s decision: the race is
   allowed to finish and both clients receive a proper `race_finished`,
   *not* the silent-hang-until-TTL behavior
   `load/multi-instance-check.md`'s own kill test already documented as
   the accepted outcome for an *ungraceful* crash. A rolling update
   sending `SIGTERM` (not `SIGKILL`) is exactly the case that decision
   was meant to handle differently from a hard kill — this step is where
   that distinction either holds up or doesn't.

## What a rolling-update failure here actually means

If step 8 instead reproduces the ungraceful-crash symptom (WS sessions
hang until the k6-equivalent client's own timeout, no `race_finished`
delivered), that means one of two things, and this spec's Verification
should say which:

- `graceful-shutdown.md`'s design is sound but its Kubernetes wiring is
  wrong — check `terminationGracePeriodSeconds` actually exceeds the
  binary's internal `Shutdown` timeout, and that the readiness flip
  really happens before `Shutdown` starts draining (a manifest/timing
  bug, fixable without touching `graceful-shutdown.md`'s own logic).
- `graceful-shutdown.md`'s design itself has a real gap — e.g. the
  `race-service` pod being rolled receives `SIGTERM` and correctly leaves
  its rooms running, but the *replacement* pod claims a different
  `INSTANCE_ID` and there's no continuity between old-pod-still-finishing
  and new-pod-already-serving that a client's own reconnect logic can't
  paper over. If so, per this project's own established convention (used
  for `k6-load-test.md`'s and `multi-instance-dev-setup.md`'s own
  findings): fix `graceful-shutdown.md`'s design and revise that file,
  don't just patch around it in this script.

## Scale knobs

| Variable | Default | Meaning |
| --- | --- | --- |
| `REPEAT_RUNS` | `6` | Same as `load/multi-instance-check.md` — how many times to repeat the core lifecycle check before the rolling-update step |

## Verification

- A full run (`REPEAT_RUNS` lifecycle checks, both `race-service` pods
  seen as owner at least once, bus-traffic assertions passing on every
  run) followed by both rolling-update passes (step 8, once for
  `race-service`, once for `ws-gateway`) completing with a proper
  `race_finished` delivered in both cases — the actual "done" bar for
  this entire phase, not just for this one spec.
- `docker compose`-based `load/multi-instance-check.sh` still passes
  unmodified — this phase adds a Kubernetes-hosted verification
  alongside the existing local-process one, it does not replace it;
  Phase 4's own local-dev verification stays the fast,
  Kubernetes-independent way to check that layer in isolation.

## Notes

- This spec's script is new, not a straight port of
  `load/multi-instance-check.sh` — reuse that script's `lib/` helpers
  (`load/lib/ws-client.js`, `reconnect-client.js`, `auth.js`) where the
  underlying HTTP/WS protocol interactions are identical, but the
  process-management layer (starting/stopping real OS processes,
  `exec`-ing subshells to get killable PIDs) doesn't apply here at all —
  `kubectl`/`kind` already own that lifecycle.
- Treat this spec's own first live run the way every other verification
  script in this project's history has been treated: expect it to find
  at least one real bug neither this plan nor the specs before it
  anticipated (`load/multi-instance-check.md`'s own "Real bugs this
  script's own first live runs caught" section is the precedent — two
  genuinely new bugs surfaced only once the real two-gateway topology
  actually ran), and document whatever it finds here or in the relevant
  upstream spec, not silently.
