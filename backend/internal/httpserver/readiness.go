package httpserver

import "sync/atomic"

// ReadinessGate lets cmd/server's SIGTERM handler flip GET /healthz to
// unready the instant the signal arrives (graceful-shutdown.md) — before
// http.Server.Shutdown even begins draining in-flight connections, so
// Kubernetes' readiness-based traffic removal starts as early as
// possible. Deliberately not shared with GET /livez: liveness must stay
// "ok" for as long as the process is actually still running and able to
// answer requests, shutting down gracefully or not.
type ReadinessGate struct {
	shuttingDown atomic.Bool
}

// MarkShuttingDown flips the gate. Safe to call more than once.
func (g *ReadinessGate) MarkShuttingDown() {
	g.shuttingDown.Store(true)
}

// ShuttingDown reports whether MarkShuttingDown has been called.
func (g *ReadinessGate) ShuttingDown() bool {
	return g.shuttingDown.Load()
}
