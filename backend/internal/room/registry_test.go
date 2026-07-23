package room

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegistry_CleansUpWhenActorSelfCancels(t *testing.T) {
	withShortNoShowTimeout(t, 50*time.Millisecond)

	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Nobody ever joins this race, so the no-show timeout
	// (race-completion/finish-race.md) fires and the actor cancels its own
	// context — Remove is never called explicitly. Registry still has to
	// notice and clean up its map entry.
	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("race-1"); !ok {
			return // cleaned up
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Registry still has race-1 after its actor self-cancelled without an explicit Remove")
}

func TestRegistry_SpawnThenGet(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spawned := reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	got, ok := reg.Get("race-1")
	if !ok {
		t.Fatal("Get(race-1) ok = false, want true after Spawn")
	}
	if got != spawned {
		t.Error("Get(race-1) returned a different *RoomActor than Spawn produced")
	}
}

func TestRegistry_Get_UnknownRace(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)

	_, ok := reg.Get("nonexistent-race")
	if ok {
		t.Error("Get(nonexistent-race) ok = true, want false")
	}
}

func TestRegistry_Remove_StopsActorAndDeregisters(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	reg.Remove("race-1")

	if _, ok := reg.Get("race-1"); ok {
		t.Error("Get(race-1) ok = true after Remove, want false")
	}

	// cancel() unblocks Run()'s ctx.Done() case — the actor's own context
	// (derived from the Spawn ctx) should now be done, proving Remove
	// actually stopped the goroutine rather than just forgetting about it.
	select {
	case <-actor.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("actor context not done after Remove — goroutine leak")
	}
}

func TestRegistry_Remove_UnknownRace_NoOp(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)

	// Must not panic when removing a race_id that was never spawned.
	reg.Remove("nonexistent-race")
}

func TestRegistry_ConcurrentGetDuringSpawn(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRaces = 50
	var wg sync.WaitGroup

	for i := range numRaces {
		raceID := fmt.Sprintf("race-%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.Spawn(ctx, raceID, 5, noopFinisher{}, noopLeaver{}, noopCanceller{})
		}()
		go func() {
			defer wg.Done()
			// May or may not have been spawned yet — just must not race.
			reg.Get(raceID)
		}()
	}
	wg.Wait()

	for i := range numRaces {
		raceID := fmt.Sprintf("race-%d", i)
		if _, ok := reg.Get(raceID); !ok {
			t.Errorf("Get(%s) ok = false after all spawns completed", raceID)
		}
	}
}

func TestRegistry_RemoveRacingGet(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRaces = 50
	for i := range numRaces {
		reg.Spawn(ctx, fmt.Sprintf("race-%d", i), 5, noopFinisher{}, noopLeaver{}, noopCanceller{})
	}

	var wg sync.WaitGroup
	for i := range numRaces {
		raceID := fmt.Sprintf("race-%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.Remove(raceID)
		}()
		go func() {
			defer wg.Done()
			// Result doesn't matter — only that this never races with Remove.
			reg.Get(raceID)
		}()
	}
	wg.Wait()

	for i := range numRaces {
		raceID := fmt.Sprintf("race-%d", i)
		if _, ok := reg.Get(raceID); ok {
			t.Errorf("Get(%s) ok = true after Remove, want false", raceID)
		}
	}
}

func TestRegistry_Count(t *testing.T) {
	reg := NewRegistry(testLogger, testTickObserver)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0 before any Spawn", got)
	}

	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})
	reg.Spawn(ctx, "race-2", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	if got := reg.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2 after two Spawns", got)
	}

	reg.Remove("race-1")
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1 after Remove", got)
	}
}

// TestRegistry_ChannelBufferUsage_SumsAcrossRooms constructs rooms directly
// (bypassing Spawn/Run, the same "no running goroutine" approach
// newTestActor uses) so inbox/broadcast channel contents are exact and
// deterministic, rather than racing a real Run() loop that would drain them.
func TestRegistry_ChannelBufferUsage_SumsAcrossRooms(t *testing.T) {
	a1 := newTestActor()
	a1.inbox = make(chan RoomEvent, 4)
	a1.inbox <- ParticipantJoined{UserID: "u1"}
	a1.broadcast = make(chan []byte, 4)
	a1.broadcast <- []byte("x")
	a1.broadcast <- []byte("y")

	a2 := newTestActor()
	a2.inbox = make(chan RoomEvent, 4)
	a2.broadcast = make(chan []byte, 4)
	a2.broadcast <- []byte("z")

	reg := &Registry{
		rooms:        map[string]*RoomActor{"race-1": a1, "race-2": a2},
		logger:       testLogger,
		tickObserver: testTickObserver,
	}

	if got := reg.InboxBufferUsage(); got != 1 {
		t.Errorf("InboxBufferUsage() = %d, want 1", got)
	}
	if got := reg.BroadcastBufferUsage(); got != 3 {
		t.Errorf("BroadcastBufferUsage() = %d, want 3", got)
	}
}
