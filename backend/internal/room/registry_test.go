package room

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegistry_SpawnThenGet(t *testing.T) {
	reg := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spawned := reg.Spawn(ctx, "race-1", "prompt", 5)

	got, ok := reg.Get("race-1")
	if !ok {
		t.Fatal("Get(race-1) ok = false, want true after Spawn")
	}
	if got != spawned {
		t.Error("Get(race-1) returned a different *RoomActor than Spawn produced")
	}
}

func TestRegistry_Get_UnknownRace(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent-race")
	if ok {
		t.Error("Get(nonexistent-race) ok = true, want false")
	}
}

func TestRegistry_Remove_StopsActorAndDeregisters(t *testing.T) {
	reg := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := reg.Spawn(ctx, "race-1", "prompt", 5)

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
	reg := NewRegistry()

	// Must not panic when removing a race_id that was never spawned.
	reg.Remove("nonexistent-race")
}

func TestRegistry_ConcurrentGetDuringSpawn(t *testing.T) {
	reg := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRaces = 50
	var wg sync.WaitGroup

	for i := range numRaces {
		raceID := fmt.Sprintf("race-%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.Spawn(ctx, raceID, "prompt", 5)
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
	reg := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRaces = 50
	for i := range numRaces {
		reg.Spawn(ctx, fmt.Sprintf("race-%d", i), "prompt", 5)
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
