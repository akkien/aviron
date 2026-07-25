package roomlocator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLocator(t *testing.T, instanceID string) (*Locator, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewLocator(client, instanceID), mr
}

func TestLocator_Claim_FirstClaimSucceeds(t *testing.T) {
	ctx := context.Background()
	l, mr := newTestLocator(t, "instance-a")

	claimed, err := l.Claim(ctx, "race-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}
	if !mr.Exists("room:race-1") {
		t.Fatal("expected room:race-1 to exist in redis")
	}
}

func TestLocator_Claim_SecondClaimByDifferentInstanceFails(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	a := NewLocator(client, "instance-a")
	b := NewLocator(client, "instance-b")

	claimedA, err := a.Claim(ctx, "race-1")
	if err != nil || !claimedA {
		t.Fatalf("expected first claim to succeed, got claimed=%v err=%v", claimedA, err)
	}

	claimedB, err := b.Claim(ctx, "race-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimedB {
		t.Fatal("expected second claim by a different instance to fail")
	}
}

func TestLocator_Refresh_ExtendsTTLWhenClaimed(t *testing.T) {
	ctx := context.Background()
	l, mr := newTestLocator(t, "instance-a")

	if _, err := l.Claim(ctx, "race-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	mr.SetTTL("room:race-1", 5*time.Second)

	if err := l.Refresh(ctx, "race-1"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if ttl := mr.TTL("room:race-1"); ttl <= 5*time.Second {
		t.Fatalf("expected TTL to be extended past 5s, got %v", ttl)
	}
}

func TestLocator_Refresh_ErrorsWhenClaimExpired(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLocator(t, "instance-a")

	if err := l.Refresh(ctx, "race-never-claimed"); err == nil {
		t.Fatal("expected an error refreshing a claim that was never made")
	}
}

func TestLocator_Release_DeletesClaim(t *testing.T) {
	ctx := context.Background()
	l, mr := newTestLocator(t, "instance-a")

	if _, err := l.Claim(ctx, "race-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := l.Release(ctx, "race-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if mr.Exists("room:race-1") {
		t.Fatal("expected room:race-1 to be deleted after Release")
	}
}

func TestLocator_Owner_ReturnsNotFoundWhenUnclaimed(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLocator(t, "instance-a")

	instanceID, ok, err := l.Owner(ctx, "race-1")
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for an unclaimed race, got instanceID=%q", instanceID)
	}
}

func TestLocator_Owner_ReturnsClaimingInstance(t *testing.T) {
	ctx := context.Background()
	l, _ := newTestLocator(t, "instance-a")

	if _, err := l.Claim(ctx, "race-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	instanceID, ok, err := l.Owner(ctx, "race-1")
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if instanceID != "instance-a" {
		t.Fatalf("expected instance-a, got %q", instanceID)
	}
}

// TestLocator_ClaimAndRelease_PublishRoomEvents is the one piece
// race-router.md directly depends on: proof that Claim/Release actually put
// the expected payloads onto room:events, not just that they mutate the key.
func TestLocator_ClaimAndRelease_PublishRoomEvents(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	sub := client.Subscribe(ctx, roomEventsChannel)
	t.Cleanup(func() { sub.Close() })
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	ch := sub.Channel()

	l := NewLocator(client, "instance-a")

	if _, err := l.Claim(ctx, "race-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	created := decodeRoomEvent(t, receiveOrTimeout(t, ch))
	if created.Type != "created" || created.RaceID != "race-1" || created.InstanceID != "instance-a" {
		t.Fatalf("unexpected created event: %+v", created)
	}

	if err := l.Release(ctx, "race-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	removed := decodeRoomEvent(t, receiveOrTimeout(t, ch))
	if removed.Type != "removed" || removed.RaceID != "race-1" || removed.InstanceID != "instance-a" {
		t.Fatalf("unexpected removed event: %+v", removed)
	}
}

func receiveOrTimeout(t *testing.T, ch <-chan *redis.Message) *redis.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a room:events message")
		return nil
	}
}

func decodeRoomEvent(t *testing.T, msg *redis.Message) roomEvent {
	t.Helper()
	var ev roomEvent
	if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
		t.Fatalf("unmarshal room event: %v", err)
	}
	return ev
}
