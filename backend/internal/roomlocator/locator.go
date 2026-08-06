// Package roomlocator makes room ownership durably visible across
// instances via Redis (project-overview.md §5): a room has exactly one
// instance that ever calls Claim for it, decided by construction (whichever
// instance received the POST /races request that spawned it) — Redis's
// only job is recording that fact for every other instance to read.
package roomlocator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// roomEventsChannel is the single shared pub/sub channel every instance
// publishes room ownership changes to — ws-gateway.md's routing cache is
// the consumer, via SubscribeRoomEvents.
const roomEventsChannel = "room:events"

// claimTTL bounds how long a claim survives without a heartbeat refresh —
// long enough that a normal ~20s refresh interval (internal/room.Registry)
// has margin, short enough that a crashed instance's rooms stop looking
// owned within a minute.
const claimTTL = 60 * time.Second

// Locator wraps a shared *redis.Client and this instance's id.
type Locator struct {
	client     *redis.Client
	instanceID string

	// lookupDuration/errorsTotal are labeled by op (the method name) —
	// owned directly by Locator and registered at construction time, for
	// the same "leaf infrastructure wrapper, not business logic" reasoning
	// roomrelay.Bus's identical fields carry (see Metrics.Registerer's doc
	// comment). outcome for lookupDuration is "ok"/"error"/(Owner-only)
	// "not_found" (metrics/metrics-parity.md).
	lookupDuration *prometheus.HistogramVec
	errorsTotal    *prometheus.CounterVec
}

// NewLocator constructs a Locator for instanceID against client,
// registering its lookup metrics into reg (typically
// *metrics.Metrics/*metrics.GatewayMetrics's own registry via Registerer()).
func NewLocator(client *redis.Client, instanceID string, reg prometheus.Registerer) *Locator {
	lookupDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "aviron_roomlocator_lookup_duration_seconds",
		Help: "Duration of a single Locator method call against Redis.",
	}, []string{"op", "outcome"})
	errorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aviron_roomlocator_errors_total",
		Help: "Locator method calls that returned an error.",
	}, []string{"op"})
	reg.MustRegister(lookupDuration, errorsTotal)

	return &Locator{
		client:         client,
		instanceID:     instanceID,
		lookupDuration: lookupDuration,
		errorsTotal:    errorsTotal,
	}
}

// observe is safe for concurrent use: prometheus.Counter/Histogram's own
// Inc/Observe already are, so no locking of Locator's own is needed.
func (l *Locator) observe(op string, start time.Time, outcome string) {
	l.lookupDuration.WithLabelValues(op, outcome).Observe(time.Since(start).Seconds())
	if outcome == "error" {
		l.errorsTotal.WithLabelValues(op).Inc()
	}
}

// RoomEvent is room:events' wire payload.
type RoomEvent struct {
	Type       string `json:"type"` // RoomEventCreated | RoomEventRemoved
	RaceID     string `json:"race_id"`
	InstanceID string `json:"instance_id"`
}

// RoomEvent.Type values.
const (
	RoomEventCreated = "created"
	RoomEventRemoved = "removed"
)

func roomKey(raceID string) string {
	return "room:" + raceID
}

// Claim records this instance as raceID's owner (SET NX EX) and publishes a
// "created" room:events notification. Returns claimed=false, err=nil if
// another instance already owns raceID — this should never happen under
// normal operation (ownership is decided by construction, not contested),
// but Claim reports it rather than silently overwriting a live claim.
func (l *Locator) Claim(ctx context.Context, raceID string) (bool, error) {
	start := time.Now()
	claimed, err := l.client.SetNX(ctx, roomKey(raceID), "instance:"+l.instanceID, claimTTL).Result()
	if err != nil {
		l.observe("claim", start, "error")
		return false, fmt.Errorf("roomlocator: claim %s: %w", raceID, err)
	}
	if !claimed {
		l.observe("claim", start, "ok")
		return false, nil
	}

	if err := l.publish(ctx, RoomEvent{Type: RoomEventCreated, RaceID: raceID, InstanceID: l.instanceID}); err != nil {
		l.observe("claim", start, "error")
		return true, err
	}
	l.observe("claim", start, "ok")
	return true, nil
}

// Refresh extends raceID's ownership TTL — called periodically by the
// owning instance's own heartbeat so the key survives for as long as the
// room keeps running, well past the initial claimTTL.
func (l *Locator) Refresh(ctx context.Context, raceID string) error {
	start := time.Now()
	stillOwned, err := l.client.Expire(ctx, roomKey(raceID), claimTTL).Result()
	if err != nil {
		l.observe("refresh", start, "error")
		return fmt.Errorf("roomlocator: refresh %s: %w", raceID, err)
	}
	if !stillOwned {
		l.observe("refresh", start, "error")
		return fmt.Errorf("roomlocator: refresh %s: claim expired before heartbeat", raceID)
	}
	l.observe("refresh", start, "ok")
	return nil
}

// Release deletes raceID's ownership record and publishes a "removed"
// room:events notification.
func (l *Locator) Release(ctx context.Context, raceID string) error {
	start := time.Now()
	if err := l.client.Del(ctx, roomKey(raceID)).Err(); err != nil {
		l.observe("release", start, "error")
		return fmt.Errorf("roomlocator: release %s: %w", raceID, err)
	}
	err := l.publish(ctx, RoomEvent{Type: RoomEventRemoved, RaceID: raceID, InstanceID: l.instanceID})
	if err != nil {
		l.observe("release", start, "error")
		return err
	}
	l.observe("release", start, "ok")
	return nil
}

// evictedKey is the Redis set ws-gateway.md's evicted-reconnect check reads
// via SISMEMBER — a separate key from roomKey, not folded into the ownership
// record itself, since the two have different lifetimes and readers.
func evictedKey(raceID string) string {
	return "race:" + raceID + ":evicted"
}

// MarkEvicted records userID as evicted from raceID — called by
// RoomActor.departParticipant (room.go) at the same moment it marks a
// participant evicted in its own in-memory state, so ws-gateway.md's
// SISMEMBER check (a direct Redis read, bypassing internal/roomrelay
// entirely — room-message-bus.md's "Evicted-reconnect checks bypass the
// bus entirely") sees the same answer. No TTL/explicit cleanup: an evicted
// set is small (bounded by a race's own participant count) and irrelevant
// once the race itself is gone, the same "not worth a second lifecycle to
// manage" call this project already made for other small, self-limited
// Redis state.
func (l *Locator) MarkEvicted(ctx context.Context, raceID, userID string) error {
	start := time.Now()
	if err := l.client.SAdd(ctx, evictedKey(raceID), userID).Err(); err != nil {
		l.observe("mark_evicted", start, "error")
		return fmt.Errorf("roomlocator: mark evicted %s/%s: %w", raceID, userID, err)
	}
	l.observe("mark_evicted", start, "ok")
	return nil
}

// IsEvicted reports whether userID was previously marked evicted from
// raceID — ws-gateway.md's own synchronous connection-time check.
func (l *Locator) IsEvicted(ctx context.Context, raceID, userID string) (bool, error) {
	start := time.Now()
	evicted, err := l.client.SIsMember(ctx, evictedKey(raceID), userID).Result()
	if err != nil {
		l.observe("is_evicted", start, "error")
		return false, fmt.Errorf("roomlocator: is evicted %s/%s: %w", raceID, userID, err)
	}
	l.observe("is_evicted", start, "ok")
	return evicted, nil
}

// Owner returns the instance id currently owning raceID, and false if no
// instance does (never claimed, or the claim expired/was released).
func (l *Locator) Owner(ctx context.Context, raceID string) (string, bool, error) {
	start := time.Now()
	val, err := l.client.Get(ctx, roomKey(raceID)).Result()
	if errors.Is(err, redis.Nil) {
		l.observe("owner", start, "not_found")
		return "", false, nil
	}
	if err != nil {
		l.observe("owner", start, "error")
		return "", false, fmt.Errorf("roomlocator: owner %s: %w", raceID, err)
	}
	l.observe("owner", start, "ok")
	return strings.TrimPrefix(val, "instance:"), true, nil
}

// SubscribeRoomEvents subscribes to room:events and returns a channel of
// decoded RoomEvents — ws-gateway.md's routing cache is the consumer, so
// this keeps the wire schema and raw *redis.Client access encapsulated in
// roomlocator rather than duplicated in cmd/ws-gateway. The returned
// channel closes once ctx is done or the underlying subscription ends;
// malformed payloads are skipped, not treated as fatal.
func (l *Locator) SubscribeRoomEvents(ctx context.Context) (<-chan RoomEvent, error) {
	sub := l.client.Subscribe(ctx, roomEventsChannel)
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, fmt.Errorf("roomlocator: subscribe room events: %w", err)
	}

	out := make(chan RoomEvent)
	go func() {
		defer close(out)
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev RoomEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (l *Locator) publish(ctx context.Context, ev RoomEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("roomlocator: marshal event: %w", err)
	}
	if err := l.client.Publish(ctx, roomEventsChannel, payload).Err(); err != nil {
		return fmt.Errorf("roomlocator: publish event: %w", err)
	}
	return nil
}
