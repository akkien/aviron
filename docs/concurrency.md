# Concurrency Notes

Patterns this codebase leans on repeatedly for keeping shared state safe
under concurrent access, and why one is preferred over the other here.

## Single-writer via channel vs. mutex-guarded shared state

When several goroutines need to update one piece of shared state — for
example, `internal/ws`'s `hub` tracking which connections are currently
attached to a room — there are two common ways to make that safe.

### Option A: mutex-guarded, set directly

Any goroutine calls the exported methods directly; a mutex serializes access
to the shared map.

```go
type hub struct {
    mu    sync.Mutex
    conns map[chan []byte]struct{}
}

func (h *hub) registerConn(c chan []byte) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.conns[c] = struct{}{}
}

func (h *hub) unregisterConn(c chan []byte) {
    h.mu.Lock()
    defer h.mu.Unlock()
    delete(h.conns, c)
}

func (h *hub) fanOut(msg []byte) {
    h.mu.Lock()
    defer h.mu.Unlock()
    for c := range h.conns {
        select {
        case c <- msg:
        default:
        }
    }
}
```

This works, but its safety depends on every call site — now and in the
future — remembering to take the lock before touching `conns`. Nothing in
the type system stops a new method from being added that forgets to.

### Option B: single-writer via channel (what this project uses)

Instead of letting every caller reach into the map, one dedicated goroutine
owns it entirely. Everyone else sends a request through a channel and lets
that goroutine make the change on their behalf.

```go
type hub struct {
    register   chan chan []byte
    unregister chan chan []byte
}

func (h *hub) registerConn(c chan []byte) {
    h.register <- c
}

func (h *hub) unregisterConn(c chan []byte) {
    h.unregister <- c
}

func (h *hub) run(broadcast <-chan []byte) {
    conns := make(map[chan []byte]struct{}) // only ever touched here
    for {
        select {
        case msg := <-broadcast:
            for c := range conns {
                select {
                case c <- msg:
                default:
                }
            }
        case c := <-h.register:
            conns[c] = struct{}{}
        case c := <-h.unregister:
            delete(conns, c)
        }
    }
}
```

`conns` has no lock at all here — `registerConn`/`unregisterConn` never touch
it directly, they just hand a value to the one goroutine (`run`) that does.
That goroutine is the *only* reader or writer of `conns`, so there's no race
to arbitrate: not "the race is made safe," but "there is no race, by
construction."

### Why this project chooses B

- **No lock to forget.** Option A's safety depends on every current and
  future call site remembering to lock before touching shared state. Option
  B makes that mistake structurally impossible — there is no code path that
  reaches `conns` except from inside `run`.
- **No risk of holding a lock across something slow.** If `fanOut`'s
  per-connection sends ever grew a blocking call under the mutex in option
  A, every concurrent `registerConn`/`unregisterConn` call would block too.
  In option B, register/unregister requests just wait their turn in a
  channel — a slow fan-out iteration can't block a registration from
  returning, because there's no shared lock between them.
- **Consistent with the rest of the codebase.** `internal/room`'s
  `RoomActor` already applies this exact pattern to its `participants` map:
  external goroutines never mutate it directly, they send a `RoomEvent` on
  `inbox` and let `Run()`'s single goroutine apply it. `hub` reuses the same
  idea for a room's connection set instead of introducing a second
  concurrency style alongside it.

The tradeoff: option B only works while the owning goroutine keeps running
to drain its channels. If `run()` ever exits while another goroutine is
blocked on `h.register <- c`, that send blocks forever — a real goroutine
leak. `internal/ws/hub.go`'s actual `registerConn`/`unregisterConn` guard
against exactly this by racing the send against a `closed` channel that's
closed right after `run()` returns:

```go
func (h *hub) registerConn(c chan []byte) {
    select {
    case h.register <- c:
    case <-h.closed:
    }
}
```

## Bug: all players got disconnected when the last player finished a race

A real bug, found via manual testing and fixed in `internal/room/room.go`,
`internal/ws/hub.go`, and `internal/ws/endpoint.go`. Left here because the
underlying mistake — racing a channel receive against an unrelated shutdown
signal in the same `select` — is a general pattern worth recognizing, not
specific to this one bug.

### Symptom

The moment the last player crossed the finish line, every player still
connected to that race — not just the one who just finished — got kicked
off the server. Instead of seeing the results screen, everyone's vehicle
froze in place and their client dropped straight to "You were disconnected
too long and have left the race" with an empty sidebar, even though the
race had genuinely just finished normally, not timed out.

### Implementation cause

`RoomActor.finishRace` sent its final messages and then tore the room down
in the same breath, with nothing forcing the former to complete before the
latter:

```go
func (r *RoomActor) finishRace(results []ParticipantResult) {
    if err := r.finisher.FinishRace(r.ctx, r.id, r.distanceMeters, results); err != nil {
        log.Printf("room %s: finish race: %v", r.id, err)
        return
    }
    r.finished = true
    // ... marshal resultsJSON ...
    select {
    case r.broadcast <- body: // race_finished
    default:
    }
    r.cancel() // tears down the room's context
}
```

`r.broadcast` is buffered (`broadcastBufferSize = 16`), so that send never
blocks — it just drops the message into the buffer and moves on immediately
to `r.cancel()`. Two things downstream both watched `r.cancel()`'s effect in
a `select`, racing it against the very channel carrying that final message:

```go
// internal/ws/hub.go — fans a room's broadcast out to every connection
func (h *hub) run(broadcast <-chan []byte, done <-chan struct{}, onClose func()) {
    for {
        select {
        case msg := <-broadcast:
            /* forward to every connection */
        case <-done: // actor.Context().Done()
            return
        }
    }
}

// internal/ws/endpoint.go — one per connection
connCtx, cancel := context.WithCancel(actor.Context())
...
func writeLoop(ctx context.Context, conn wsConn, connCh <-chan []byte) {
    for {
        select {
        case msg := <-connCh:
            /* write msg to the socket */
        case <-ctx.Done():
            return
        }
    }
}
```

### Why it happened

By the time `r.cancel()` runs, the final message is already sitting in
`r.broadcast`'s buffer — so `broadcast`/`connCh` (non-empty) and
`done`/`ctx.Done()` (just closed) become ready at essentially the same
moment. Go's `select` picks a ready case *pseudo-randomly* when more than
one is ready — it does **not** prefer whichever channel received a value
first. So `hub.run` and `writeLoop` could each independently pick their
`done`/`ctx.Done()` case and return, leaving the final `race_state`/
`race_finished` message stuck, unread, in a channel nobody would ever drain
again.

Worse, `connCtx` was derived *directly* from the room's own context
(`context.WithCancel(actor.Context())`) — and **every connection attached to
that room derives its own `connCtx` from that same `actor.Context()`**, so
`r.cancel()` didn't just race one connection's shutdown, it simultaneously
raced every player's `writeLoop` in the room at once. That's the "the two
races were independent and stacked" problem multiplied across everyone still
connected, not a one-off affecting only the finisher: fixing only the hub
wasn't enough, because each of N connections' `writeLoop` could independently
lose the message one layer further down.

> Once a given `writeLoop` returned without ever writing that final message,
> its deferred `cancel()` unblocked that connection's `readLoop`, both of its
> goroutines exited, and `serveConn` called `conn.Close(...)` — actively
> closing that player's real WebSocket connection from the server side.
> Since this played out independently for every connection in the room, the
> entire room could be kicked off at once, not just the player who happened
> to finish last. The browser has no way to distinguish "the server closed
> us because the race legitimately finished" from "the connection just
> dropped." Since none of the clients ever received/parsed a `race_finished`
> message body, their `onclose` handlers had no way to know this was
> expected, so each fell into its own reconnect logic, failed against a room
> that was already torn down and removed from the registry, exhausted its
> retries, and surfaced as "disconnected too long" — for everyone, all at
> once.

### The fix

**1. `hub.run` drains `broadcast` before actually returning on `done`:**

```go
case <-done:
    for {
        select {
        case msg := <-broadcast:
            for c := range conns {
                select {
                case c <- msg:
                default:
                }
            }
        default:
            return
        }
    }
```

**2. `serveConn` no longer derives the connection's context from the room's
context, and `writeLoop` drains on `hub.closed` instead of racing `connCh`
against the room's own shutdown signal:**

```go
// Deliberately context.Background(), not context.WithCancel(actor.Context()) —
// see writeLoop's hubClosed case.
connCtx, cancel := context.WithCancel(context.Background())
...
writeLoop(connCtx, hub.closed, conn, connCh)

func writeLoop(ctx context.Context, hubClosed <-chan struct{}, conn wsConn, connCh <-chan []byte) {
    for {
        select {
        case msg := <-connCh:
            if !writeMsg(ctx, conn, msg) {
                return
            }
        case <-hubClosed:
            for {
                select {
                case msg := <-connCh:
                    if !writeMsg(ctx, conn, msg) {
                        return
                    }
                default:
                    return
                }
            }
        case <-ctx.Done():
            return
        }
    }
}
```

`connCtx` now only ever gets cancelled for this connection's *own* reasons
(a read or write error) — never directly by the room finishing.

### Why this fixes it

`hub.closed` is only closed *after* `hub.run`'s loop has returned — which,
thanks to fix 1, only happens after it has fully drained `broadcast` and
forwarded every pending message into every connection's `connCh`. That's a
genuine happens-before relationship (Go's channel-close semantics
guarantee it), not a second coin flip: by the time any `writeLoop` observes
`hubClosed`, the final message is *already* sitting in that connection's
`connCh` buffer, guaranteed.

Because `connCtx` is no longer tied to the room's context, `writeLoop`'s
`ctx.Done()` case can no longer fire concurrently with `hubClosed` for the
"room finished" reason — the only remaining race in that `select` is
between a genuinely new message and a genuinely independent per-connection
error, which is fine to resolve either way. And `writeLoop`'s `hubClosed`
branch drains `connCh` exhaustively (via the inner non-blocking loop) before
returning, exactly mirroring fix 1 one layer down.

The result: the last messages a finishing room ever sends are now
guaranteed to reach every still-connected client before that client's
connection is torn down, regardless of how the Go scheduler happens to
interleave the hub and connection goroutines.

Proven with `internal/ws/endpoint_test.go`'s
`TestServeConn_FinishingRaceDeliversFinalStateBeforeClosing`: a solo racer
with `distanceMeters = 1` makes the race finish on the very first telemetry
message, forcing this exact race deterministically. Confirmed failing 20/20
runs against the old code, passing 50/50 against the fix.
