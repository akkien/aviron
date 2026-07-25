package room

import (
	"context"
	"sync"
	"testing"
	"time"
)

// withShortHeartbeatInterval temporarily shortens heartbeatInterval so
// tests don't have to wait a real 20s, restoring it afterward.
func withShortHeartbeatInterval(t *testing.T, d time.Duration) {
	t.Helper()
	original := heartbeatInterval
	heartbeatInterval = d
	t.Cleanup(func() { heartbeatInterval = original })
}

// spyLocator records every Claim/Refresh/Release call it receives, for
// asserting Registry actually wires Spawn/heartbeat/cleanup into
// RoomLocator (redis-room-registry.md) rather than just accepting the
// parameter and never calling it.
type spyLocator struct {
	mu        sync.Mutex
	claimed   []string
	refreshed []string
	released  []string
}

func (s *spyLocator) Claim(ctx context.Context, raceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = append(s.claimed, raceID)
	return true, nil
}

func (s *spyLocator) Refresh(ctx context.Context, raceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshed = append(s.refreshed, raceID)
	return nil
}

func (s *spyLocator) Release(ctx context.Context, raceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, raceID)
	return nil
}

func (s *spyLocator) refreshCount(raceID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range s.refreshed {
		if id == raceID {
			n++
		}
	}
	return n
}

func TestRegistry_Spawn_ClaimsOwnershipImmediately(t *testing.T) {
	spy := &spyLocator{}
	reg := NewRegistry(testLogger, testTickObserver, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.claimed) != 1 || spy.claimed[0] != "race-1" {
		t.Fatalf("claimed = %v, want [race-1]", spy.claimed)
	}
}

func TestRegistry_Spawn_HeartbeatRefreshesOwnership(t *testing.T) {
	withShortHeartbeatInterval(t, 20*time.Millisecond)

	spy := &spyLocator{}
	reg := NewRegistry(testLogger, testTickObserver, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if spy.refreshCount("race-1") >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected at least 2 heartbeat refreshes for race-1, got %d", spy.refreshCount("race-1"))
}

func TestRegistry_Remove_ReleasesOwnership(t *testing.T) {
	spy := &spyLocator{}
	reg := NewRegistry(testLogger, testTickObserver, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})
	reg.Remove("race-1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		spy.mu.Lock()
		released := len(spy.released) == 1 && spy.released[0] == "race-1"
		spy.mu.Unlock()
		if released {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected Release(race-1) to be called after Remove")
}

func TestRegistry_ActorSelfCancel_ReleasesOwnership(t *testing.T) {
	withShortNoShowTimeout(t, 50*time.Millisecond)

	spy := &spyLocator{}
	reg := NewRegistry(testLogger, testTickObserver, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Nobody ever joins, so the no-show timeout fires and the actor cancels
	// itself — Remove is never called explicitly, same scenario as
	// TestRegistry_CleansUpWhenActorSelfCancels.
	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		spy.mu.Lock()
		released := len(spy.released) == 1 && spy.released[0] == "race-1"
		spy.mu.Unlock()
		if released {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected Release(race-1) to be called after the actor self-cancelled")
}
