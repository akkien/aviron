package roomrelay

import (
	"context"
	"sync"
)

// fakeSubscriptionBuffer bounds each FakeBus subscription's channel, the
// same role subscriptionBuffer plays for the real NATS-backed Bus.
const fakeSubscriptionBuffer = 16

// FakeBus is an in-memory bus with the same publish/subscribe shape as Bus,
// for tests that don't need a real NATS connection — the role miniredis
// plays for internal/roomlocator, scoped to just this package's fan-out
// semantics rather than a full protocol implementation. A slow or
// no-longer-listening receiver never blocks Publish, mirroring the
// at-most-once delivery this bus is designed around (room-message-bus.md's
// Notes): a full subscriber channel simply drops the message.
type FakeBus struct {
	mu  sync.Mutex
	in  map[string][]chan InboundEnvelope
	out map[string][]chan OutboundEnvelope
}

// NewFakeBus constructs an empty FakeBus.
func NewFakeBus() *FakeBus {
	return &FakeBus{
		in:  make(map[string][]chan InboundEnvelope),
		out: make(map[string][]chan OutboundEnvelope),
	}
}

// PublishIn fans env out to every current SubscribeIn subscriber for raceID.
func (b *FakeBus) PublishIn(ctx context.Context, raceID string, env InboundEnvelope) error {
	return fakePublish(ctx, &b.mu, b.in, raceID, env)
}

// PublishOut fans env out to every current SubscribeOut subscriber for
// raceID.
func (b *FakeBus) PublishOut(ctx context.Context, raceID string, env OutboundEnvelope) error {
	return fakePublish(ctx, &b.mu, b.out, raceID, env)
}

// SubscribeIn registers a new inbound subscriber for raceID. The returned
// channel closes once ctx is done or unsubscribe is called, whichever
// happens first.
func (b *FakeBus) SubscribeIn(ctx context.Context, raceID string) (<-chan InboundEnvelope, func(), error) {
	return fakeSubscribe(ctx, &b.mu, b.in, raceID)
}

// SubscribeOut registers a new outbound subscriber for raceID. Same
// lifecycle contract as SubscribeIn.
func (b *FakeBus) SubscribeOut(ctx context.Context, raceID string) (<-chan OutboundEnvelope, func(), error) {
	return fakeSubscribe(ctx, &b.mu, b.out, raceID)
}

func fakePublish[T any](ctx context.Context, mu *sync.Mutex, subs map[string][]chan T, raceID string, env T) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ch := range subs[raceID] {
		select {
		case ch <- env:
		default:
		}
	}
	return nil
}

func fakeSubscribe[T any](ctx context.Context, mu *sync.Mutex, subs map[string][]chan T, raceID string) (<-chan T, func(), error) {
	ch := make(chan T, fakeSubscriptionBuffer)
	stop := make(chan struct{})

	mu.Lock()
	subs[raceID] = append(subs[raceID], ch)
	mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			close(stop)
			mu.Lock()
			subs[raceID] = removeChan(subs[raceID], ch)
			close(ch)
			mu.Unlock()
		})
	}

	go func() {
		select {
		case <-ctx.Done():
			unsubscribe()
		case <-stop:
		}
	}()

	return ch, unsubscribe, nil
}

func removeChan[T any](chans []chan T, target chan T) []chan T {
	for i, ch := range chans {
		if ch == target {
			return append(chans[:i], chans[i+1:]...)
		}
	}
	return chans
}
