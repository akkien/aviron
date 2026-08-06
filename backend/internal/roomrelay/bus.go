package roomrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
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

	// publishTotal/publishErrors/publishDuration are labeled by
	// subject_kind ("in"/"out"), never race_id, matching this project's
	// existing cardinality discipline (metrics/metrics-parity.md). Owned
	// directly by Bus and registered at construction time — roomrelay is
	// already a leaf wrapper directly around *nats.Conn, not business
	// logic with the transport-free layering concern room-actor-core.md
	// raised for internal/room, so a direct prometheus/client_golang
	// import here doesn't compromise anything that reasoning protects; see
	// Metrics.Registerer's doc comment for the full comparison.
	publishTotal    *prometheus.CounterVec
	publishErrors   *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
}

// NewBus constructs a Bus around nc, registering its publish metrics into
// reg (typically *metrics.Metrics/*metrics.GatewayMetrics's own registry
// via Registerer()).
func NewBus(nc *nats.Conn, reg prometheus.Registerer) *Bus {
	publishTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aviron_roomrelay_publish_total",
		Help: "Publish attempts on a room's NATS subjects.",
	}, []string{"subject_kind"})
	publishErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aviron_roomrelay_publish_errors_total",
		Help: "Publish attempts on a room's NATS subjects that returned an error.",
	}, []string{"subject_kind"})
	publishDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "aviron_roomrelay_publish_duration_seconds",
		Help: "Duration of a single publish call on a room's NATS subjects.",
	}, []string{"subject_kind"})
	reg.MustRegister(publishTotal, publishErrors, publishDuration)

	return &Bus{
		nc:              nc,
		publishTotal:    publishTotal,
		publishErrors:   publishErrors,
		publishDuration: publishDuration,
	}
}

// PublishIn publishes env on raceID's inbound subject.
func (b *Bus) PublishIn(ctx context.Context, raceID string, env InboundEnvelope) error {
	start := time.Now()
	err := publish(ctx, b.nc, inSubject(raceID), env)
	b.observePublish("in", start, err)
	return err
}

// PublishOut publishes env on raceID's outbound subject.
func (b *Bus) PublishOut(ctx context.Context, raceID string, env OutboundEnvelope) error {
	start := time.Now()
	err := publish(ctx, b.nc, outSubject(raceID), env)
	b.observePublish("out", start, err)
	return err
}

// observePublish is safe for concurrent use: prometheus.Counter/Histogram's
// own Inc/Observe already are, so no locking of Bus's own is needed.
func (b *Bus) observePublish(subjectKind string, start time.Time, err error) {
	b.publishTotal.WithLabelValues(subjectKind).Inc()
	b.publishDuration.WithLabelValues(subjectKind).Observe(time.Since(start).Seconds())
	if err != nil {
		b.publishErrors.WithLabelValues(subjectKind).Inc()
	}
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
