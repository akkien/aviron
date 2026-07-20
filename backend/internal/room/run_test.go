package room

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRoomActor_Run_BroadcastsOnTick(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", "some prompt text", 3, broadcast)
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	go r.Run()

	select {
	case body := <-broadcast:
		var msg RaceStateMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if msg.Type != "race_state" {
			t.Errorf("Type = %q, want %q", msg.Type, "race_state")
		}
		if len(msg.Participants) != 1 {
			t.Fatalf("Participants = %d, want 1", len(msg.Participants))
		}
		if msg.Participants[0].UserID != "user-1" {
			t.Errorf("UserID = %q, want %q", msg.Participants[0].UserID, "user-1")
		}
		if msg.Participants[0].Rank != 1 {
			t.Errorf("Rank = %d, want 1", msg.Participants[0].Rank)
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast received within 1s (expected a tick every 250ms)")
	}
}

func TestRoomActor_Run_StopsOnContextCancel(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", "prompt", 3, broadcast)

	done := make(chan struct{})
	go func() {
		r.Run()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after context cancellation — goroutine leak")
	}
}

func TestRoomActor_Run_ConcurrentInboxSenders(t *testing.T) {
	broadcast := make(chan []byte, 256)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", "prompt", 100, broadcast)

	const numParticipants = 5
	const eventsPerParticipant = 50

	// Seed participants before starting Run() — no goroutine is running yet,
	// so calling applyEvent directly here is safe.
	for i := range numParticipants {
		r.applyEvent(ParticipantJoined{
			UserID:      fmt.Sprintf("user-%d", i),
			DisplayName: fmt.Sprintf("User %d", i),
		})
	}

	done := make(chan struct{})
	go func() {
		r.Run()
		close(done)
	}()

	// Drain broadcasts concurrently so broadcastSnapshot's non-blocking send
	// never has a reason to drop a tick while the inbox is still filling.
	stopDraining := make(chan struct{})
	go func() {
		for {
			select {
			case <-broadcast:
			case <-stopDraining:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := range numParticipants {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			userID := fmt.Sprintf("user-%d", idx)
			for seq := 1; seq <= eventsPerParticipant; seq++ {
				r.inbox <- TelemetryReceived{UserID: userID, Seq: seq, WordsCorrect: seq}
			}
		}(i)
	}
	wg.Wait()

	// Give the single-writer goroutine time to drain the inbox before we
	// stop it — cancelling immediately could race a still-buffered event
	// against ctx.Done() inside Run()'s select.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
	close(stopDraining)

	// Run() has returned, so the actor goroutine is provably done touching
	// participants — reading it directly here is safe now.
	for i := range numParticipants {
		userID := fmt.Sprintf("user-%d", i)
		p, ok := r.participants[userID]
		if !ok {
			t.Fatalf("participants[%s] missing", userID)
		}
		if p.WordsCorrect != eventsPerParticipant {
			t.Errorf("%s: WordsCorrect = %d, want %d", userID, p.WordsCorrect, eventsPerParticipant)
		}
		if p.LastSeq != eventsPerParticipant {
			t.Errorf("%s: LastSeq = %d, want %d", userID, p.LastSeq, eventsPerParticipant)
		}
	}
}
