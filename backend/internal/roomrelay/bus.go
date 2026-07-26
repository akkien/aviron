package roomrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
)

// subscriptionBuffer bounds how many not-yet-decoded messages a single
// subscription's internal nats.Msg channel holds before nats.go itself
// starts applying its own slow-consumer handling — sized generously since a
// room's own traffic is bounded by human typing speed (project-overview.md
// §13), not a tight budget.
const subscriptionBuffer = 64

// Bus wraps a shared *nats.Conn to publish/subscribe InboundEnvelope and
// OutboundEnvelope on a race's two subjects (room.{race_id}.in/.out).
// nc.Publish is safe for concurrent use from multiple goroutines, so Bus
// needs no locking of its own.
type Bus struct {
	nc *nats.Conn
}

// NewBus constructs a Bus around nc.
func NewBus(nc *nats.Conn) *Bus {
	return &Bus{nc: nc}
}

// PublishIn publishes env on raceID's inbound subject.
func (b *Bus) PublishIn(ctx context.Context, raceID string, env InboundEnvelope) error {
	return publish(ctx, b.nc, inSubject(raceID), env)
}

// PublishOut publishes env on raceID's outbound subject.
func (b *Bus) PublishOut(ctx context.Context, raceID string, env OutboundEnvelope) error {
	return publish(ctx, b.nc, outSubject(raceID), env)
}

// SubscribeIn subscribes to raceID's inbound subject. The returned channel
// closes once ctx is done, the subscription ends, or unsubscribe is called
// — whichever happens first; malformed payloads are skipped, not treated as
// fatal, mirroring internal/roomlocator.SubscribeRoomEvents.
func (b *Bus) SubscribeIn(ctx context.Context, raceID string) (<-chan InboundEnvelope, func(), error) {
	out := make(chan InboundEnvelope)
	unsubscribe, err := subscribe(ctx, b.nc, inSubject(raceID), out)
	if err != nil {
		return nil, nil, err
	}
	return out, unsubscribe, nil
}

// SubscribeOut subscribes to raceID's outbound subject. Same lifecycle
// contract as SubscribeIn.
func (b *Bus) SubscribeOut(ctx context.Context, raceID string) (<-chan OutboundEnvelope, func(), error) {
	out := make(chan OutboundEnvelope)
	unsubscribe, err := subscribe(ctx, b.nc, outSubject(raceID), out)
	if err != nil {
		return nil, nil, err
	}
	return out, unsubscribe, nil
}

func publish[T any](ctx context.Context, nc *nats.Conn, subject string, env T) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("roomrelay: marshal envelope: %w", err)
	}
	if err := nc.Publish(subject, payload); err != nil {
		return fmt.Errorf("roomrelay: publish %s: %w", subject, err)
	}
	return nil
}

func subscribe[T any](ctx context.Context, nc *nats.Conn, subject string, out chan T) (func(), error) {
	msgs := make(chan *nats.Msg, subscriptionBuffer)
	sub, err := nc.ChanSubscribe(subject, msgs)
	if err != nil {
		return nil, fmt.Errorf("roomrelay: subscribe %s: %w", subject, err)
	}

	done := make(chan struct{})
	var once sync.Once
	unsubscribe := func() { once.Do(func() { close(done) }) }

	go func() {
		defer close(out)
		defer func() { _ = sub.Unsubscribe() }()
		for {
			select {
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				var env T
				if err := json.Unmarshal(msg.Data, &env); err != nil {
					continue
				}
				select {
				case out <- env:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()

	return unsubscribe, nil
}
