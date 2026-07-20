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
