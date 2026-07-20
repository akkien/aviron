package room

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

type ParticipantState struct {
	UserID         string
	DisplayName    string
	WordsCorrect   int
	LastSeq        int
	ConnectedAt    time.Time
	DisconnectedAt *time.Time
}

type RoomActor struct {
	id             string
	participants   map[string]*ParticipantState
	promptText     string
	distanceMeters int
	tickCount      int64
	inbox          chan RoomEvent
	broadcast      chan []byte
	ctx            context.Context
	cancel         context.CancelFunc
}

// NewRoomActor constructs a room actor for race id, seeded with the already
// generated promptText/distanceMeters from the race row. Call go actor.Run()
// to start it — spawning and tracking instances is room-registry.md's job,
// not this constructor's.
func NewRoomActor(ctx context.Context, id, promptText string, distanceMeters int, broadcast chan []byte) *RoomActor {
	actorCtx, cancel := context.WithCancel(ctx)
	return &RoomActor{
		id:             id,
		participants:   make(map[string]*ParticipantState),
		promptText:     promptText,
		distanceMeters: distanceMeters,
		inbox:          make(chan RoomEvent, 64), // generous buffer: a burst of telemetry must not block reader goroutines
		broadcast:      broadcast,
		ctx:            actorCtx,
		cancel:         cancel,
	}
}

// Broadcast returns the channel this actor sends race_state snapshots on.
// The room registry creates the underlying channel at Spawn time; the
// WebSocket fan-out (websocket/ws-endpoint.md) reads from it to relay
// messages to every connection attached to this room.
func (r *RoomActor) Broadcast() <-chan []byte {
	return r.broadcast
}

// Context returns this actor's context, cancelled when the room closes
// (race finished, or the registry removes it). websocket/ws-endpoint.md
// derives each connection's own context from this one, so a connection is
// cut loose the moment its room goes away.
func (r *RoomActor) Context() context.Context {
	return r.ctx
}

// Send enqueues ev onto the actor's inbox for its single-writer Run loop to
// apply. It's the only way code outside this package may feed the actor an
// event — callers (e.g. websocket/ws-endpoint.md's reader goroutine) never
// touch participants directly. The select against ctx.Done() matters: once
// Run has returned, nothing drains inbox anymore, so a plain unguarded send
// here would block forever and leak the calling goroutine.
func (r *RoomActor) Send(ev RoomEvent) {
	select {
	case r.inbox <- ev:
	case <-r.ctx.Done():
	}
}

// Run is the room actor's single-writer loop: participants is only ever
// read or mutated from inside this goroutine. Every other goroutine must
// send a RoomEvent on r.inbox instead of touching RoomActor fields directly.
func (r *RoomActor) Run() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ev := <-r.inbox:
			r.applyEvent(ev)
		case <-ticker.C:
			r.broadcastSnapshot()
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *RoomActor) applyEvent(ev RoomEvent) {
	switch e := ev.(type) {
	case ParticipantJoined:
		r.participants[e.UserID] = &ParticipantState{
			UserID:      e.UserID,
			DisplayName: e.DisplayName,
			ConnectedAt: time.Now(),
		}
		// Broadcast immediately rather than waiting for the next tick (up to
		// 250ms away) — websocket/protocol.md's join_race message is meant to
		// get the newly-attached client a snapshot right away, not leave it
		// looking at nothing until the ticker fires.
		r.broadcastSnapshot()
	case TelemetryReceived:
		p, ok := r.participants[e.UserID]
		if !ok || e.Seq <= p.LastSeq {
			return // unknown participant, or stale/duplicate/out-of-order — drop silently
		}
		p.WordsCorrect = e.WordsCorrect
		p.LastSeq = e.Seq
	case ParticipantDisconnected:
		// DisconnectedAt is set here so grace-period.md's timer logic has
		// something to act on; the timer itself belongs to that feature.
		if p, ok := r.participants[e.UserID]; ok {
			now := time.Now()
			p.DisconnectedAt = &now
		}
	}
}

// ParticipantStateJSON and RaceStateMessage are exported (rather than kept
// package-private) so websocket/protocol.md's internal/ws package can reuse
// them for its outbound race_state encoding instead of redeclaring an
// identical shape — internal/ws already depends on this package for
// RoomEvent, so this keeps that dependency one-directional.
type ParticipantStateJSON struct {
	UserID    string  `json:"user_id"`
	DistanceM float64 `json:"distance_m"`
	Rank      int     `json:"rank"`
}

type RaceStateMessage struct {
	Type         string                 `json:"type"`
	Tick         int64                  `json:"tick"`
	Participants []ParticipantStateJSON `json:"participants"`
}

func (r *RoomActor) broadcastSnapshot() {
	ranked := make([]*ParticipantState, 0, len(r.participants))
	for _, p := range r.participants {
		ranked = append(ranked, p)
	}
	// Stable sort: ties (equal WordsCorrect) keep insertion order rather
	// than an arbitrary map-iteration order or some other tiebreaker.
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].WordsCorrect > ranked[j].WordsCorrect
	})

	r.tickCount++
	msg := RaceStateMessage{Type: "race_state", Tick: r.tickCount}
	for i, p := range ranked {
		msg.Participants = append(msg.Participants, ParticipantStateJSON{
			UserID:    p.UserID,
			DistanceM: float64(p.WordsCorrect),
			Rank:      i + 1,
		})
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return // can't happen with these field types, but broadcastSnapshot must not panic the actor loop
	}
	select {
	case r.broadcast <- body:
	default:
		// The receiving channel is full — dropped, not blocked. Real
		// per-connection backpressure is websocket/ws-endpoint.md's concern;
		// this actor must never stall waiting on a slow consumer.
	}
}
