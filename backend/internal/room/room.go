package room

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"
)

// gracePeriodDuration is how long a disconnected participant's state is
// kept before they're removed for good (reconnection/grace-period.md). A
// var, not a const, so tests can shorten it instead of sleeping the real
// 30s — see TestRoomActor_Run_GracePeriodExpiry_RemovesParticipant.
var gracePeriodDuration = 30 * time.Second

type ParticipantState struct {
	UserID            string
	DisplayName       string
	WordsCorrect      int
	LastSeq           int
	ConnectedAt       time.Time
	DisconnectedAt    *time.Time
	DisconnectedCount int
	// graceTimer fires ParticipantLeft if this participant doesn't reconnect
	// in time. Stopped and cleared on reconnect so a stale expiry can never
	// remove someone who's since reattached.
	graceTimer *time.Timer
	// FinishedAt/FinishRank are set together, once, the moment WordsCorrect
	// reaches the room's distanceMeters (race-completion/finish-race.md).
	FinishedAt *time.Time
	FinishRank *int
}

type RoomActor struct {
	id           string
	participants map[string]*ParticipantState
	// evicted tracks user_ids removed via grace-period expiry, so a
	// too-late reconnect attempt can be rejected by websocket/ws-endpoint.md
	// instead of silently rejoining as a fresh participant.
	evicted        map[string]struct{}
	promptText     string
	distanceMeters int
	tickCount      int64
	inbox          chan RoomEvent
	broadcast      chan []byte
	ctx            context.Context
	cancel         context.CancelFunc
	// finisher persists final results once the race completes
	// (race-completion/finish-race.md) — see finish.go.
	finisher RaceFinisher
	// startedAt is this actor's own construction time, used as the baseline
	// for FinishTimeMs. Not literally races.started_at (that UPDATE and this
	// Spawn happen milliseconds apart, in the same handler) — close enough
	// for a side project, not worth threading the DB timestamp through.
	startedAt time.Time
	// finished guards against calling finisher.FinishRace more than once —
	// belt-and-suspenders on top of checkRaceFinished's own event-transition
	// guards (see there for why a double-call shouldn't be reachable anyway).
	finished bool
}

// NewRoomActor constructs a room actor for race id, seeded with the already
// generated promptText/distanceMeters from the race row. Call go actor.Run()
// to start it — spawning and tracking instances is room-registry.md's job,
// not this constructor's.
func NewRoomActor(ctx context.Context, id, promptText string, distanceMeters int, broadcast chan []byte, finisher RaceFinisher) *RoomActor {
	actorCtx, cancel := context.WithCancel(ctx)
	r := &RoomActor{
		id:             id,
		participants:   make(map[string]*ParticipantState),
		evicted:        make(map[string]struct{}),
		promptText:     promptText,
		distanceMeters: distanceMeters,
		inbox:          make(chan RoomEvent, 64), // generous buffer: a burst of telemetry must not block reader goroutines
		broadcast:      broadcast,
		ctx:            actorCtx,
		cancel:         cancel,
		finisher:       finisher,
		startedAt:      time.Now(),
	}
	time.AfterFunc(noShowTimeoutDuration, func() {
		r.Send(noShowTimeout{})
	})
	return r
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

// evictionQuery is a RoomEvent only so it can travel through inbox — unlike
// every other event, it doesn't mutate room state; applyEvent just answers
// it over Reply. Reply is expected to be buffered (size 1) so applyEvent's
// send can never block the single-writer loop on a caller that stopped
// listening.
type evictionQuery struct {
	UserID string
	Reply  chan<- bool
}

func (evictionQuery) isRoomEvent() {}

// IsEvicted reports whether userID's grace period already expired in this
// room. websocket/ws-endpoint.md calls this during the WS handshake, before
// upgrading a reconnect attempt, so a too-late reconnect gets rejected
// instead of silently rejoining as a fresh participant. This is the actor's
// first synchronous query rather than a fire-and-forget event — still
// answered from inside applyEvent, the only code allowed to read this
// state, via a reply channel rather than a direct field read.
func (r *RoomActor) IsEvicted(userID string) bool {
	reply := make(chan bool, 1)
	select {
	case r.inbox <- evictionQuery{UserID: userID, Reply: reply}:
	case <-r.ctx.Done():
		return false // room's gone; there's nothing left to be evicted from
	}
	select {
	case evicted := <-reply:
		return evicted
	case <-r.ctx.Done():
		return false
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
		if existing, ok := r.participants[e.UserID]; ok {
			// Already known — whether reconnecting after a disconnect or
			// just a duplicate join_race from an already-connected client
			// (e.g. two tabs, a client retry) — reuse existing progress
			// (WordsCorrect/LastSeq) instead of resetting it like a fresh
			// join would.
			if existing.DisconnectedAt != nil {
				// Reconnect within the grace period specifically: cancel the
				// pending expiry timer so it can't remove them now they're
				// back, and clear the disconnected marker.
				if existing.graceTimer != nil {
					existing.graceTimer.Stop()
					existing.graceTimer = nil
				}
				existing.DisconnectedAt = nil
			}
		} else {
			r.participants[e.UserID] = &ParticipantState{
				UserID:      e.UserID,
				DisplayName: e.DisplayName,
				ConnectedAt: time.Now(),
			}
		}
		// Broadcast immediately rather than waiting for the next tick (up to
		// 250ms away) — websocket/protocol.md's join_race message is meant to
		// get the newly-attached client a snapshot right away, not leave it
		// looking at nothing until the ticker fires. Also doubles as the
		// reconnecting client's own resync, since it's already registered
		// with the room's broadcast fan-out by the time this event applies.
		r.broadcastSnapshot()
	case TelemetryReceived:
		p, ok := r.participants[e.UserID]
		if !ok || e.Seq <= p.LastSeq {
			return // unknown participant, or stale/duplicate/out-of-order — drop silently
		}
		p.WordsCorrect = e.WordsCorrect
		p.LastSeq = e.Seq
		if p.FinishRank == nil && p.WordsCorrect >= r.distanceMeters {
			// race-completion/finish-race.md: a participant individually
			// "finishes" the moment they reach the target, in finishing
			// order — first to reach it is rank 1. Counting existing
			// FinishRanks (rather than a separate counter field) keeps this
			// self-contained in the one place rank is ever assigned.
			now := time.Now()
			p.FinishedAt = &now
			rank := 1
			for _, other := range r.participants {
				if other.FinishRank != nil {
					rank++
				}
			}
			p.FinishRank = &rank
			r.checkRaceFinished()
		}
	case ParticipantDisconnected:
		p, ok := r.participants[e.UserID]
		if !ok {
			return
		}
		now := time.Now()
		p.DisconnectedAt = &now
		p.DisconnectedCount++
		// Each disconnect (re)starts the grace-period timer — stop any
		// earlier one first (reconnected, then dropped again) so only one
		// is ever pending per participant.
		if p.graceTimer != nil {
			p.graceTimer.Stop()
		}
		userID := e.UserID
		p.graceTimer = time.AfterFunc(gracePeriodDuration, func() {
			r.Send(ParticipantLeft{UserID: userID})
		})
	case ParticipantLeft:
		// Guard against a reconnect and an already-fired timer racing each
		// other: only honor this if the participant is still the one who
		// disconnected (DisconnectedAt != nil). If they reconnected in the
		// gap between the timer firing and this event being applied, the
		// ParticipantJoined case above already cleared DisconnectedAt and
		// stopped the timer — this stale event must not remove them anyway.
		if p, ok := r.participants[e.UserID]; ok && p.DisconnectedAt != nil {
			delete(r.participants, e.UserID)
			r.evicted[e.UserID] = struct{}{}
			r.checkRaceFinished()
		}
	case evictionQuery:
		_, evicted := r.evicted[e.UserID]
		select {
		case e.Reply <- evicted:
		default:
		}
	case noShowTimeout:
		// A no-op if anyone has joined by now — checkRaceFinished only acts
		// when participants is empty or everyone remaining has finished.
		r.checkRaceFinished()
	}
}

// checkRaceFinished is race-completion/finish-race.md's finish condition:
// the race is over once there are zero live participants remaining, either
// because everyone who connected eventually left (grace period expired for
// each) or nobody finished (participants is empty). Called from every event
// that could produce that transition — TelemetryReceived (someone just
// finished), ParticipantLeft (someone just got evicted), and noShowTimeout
// (nobody ever joined) — never from ParticipantJoined/ParticipantDisconnected
// /evictionQuery, none of which can complete a race by themselves.
func (r *RoomActor) checkRaceFinished() {
	if r.finished {
		return
	}
	for _, p := range r.participants {
		if p.FinishRank == nil {
			return // still someone connected-and-racing or in their grace period
		}
	}

	results := make([]ParticipantResult, 0, len(r.participants))
	for _, p := range r.participants {
		var finishTimeMs int64
		if p.FinishedAt != nil {
			finishTimeMs = p.FinishedAt.Sub(r.startedAt).Milliseconds()
		}
		results = append(results, ParticipantResult{
			UserID:       p.UserID,
			FinishRank:   p.FinishRank,
			FinishTimeMs: &finishTimeMs,
			// AvgPaceWatt: no per-tick pace tracking exists anywhere in this
			// codebase yet (internal/ws's ClientMessage.PaceWatt is decoded
			// but never forwarded into TelemetryReceived) — left at the zero
			// value rather than building that infrastructure as a side
			// effect of this feature.
			DisconnectedCount: p.DisconnectedCount,
		})
	}
	r.finishRace(results)
}

// finishRace persists results, notifies clients, and tears the room down.
// Called only from checkRaceFinished, always from inside applyEvent — the
// single-writer goroutine — so this blocks Run()'s select loop for the
// duration of the Postgres write, which is fine: a finishing race has
// nothing left to process concurrently anyway.
func (r *RoomActor) finishRace(results []ParticipantResult) {
	if err := r.finisher.FinishRace(r.ctx, r.id, r.distanceMeters, results); err != nil {
		// No retry: this is a side project's accepted gap, not a production
		// resilience story. Logged and left running rather than torn down,
		// so at least the room doesn't silently vanish if Postgres hiccups.
		log.Printf("room %s: finish race: %v", r.id, err)
		return
	}
	r.finished = true

	resultsJSON := make([]RaceResultJSON, len(results))
	for i, res := range results {
		resultsJSON[i] = RaceResultJSON{
			UserID:       res.UserID,
			FinishRank:   res.FinishRank,
			FinishTimeMs: res.FinishTimeMs,
			AvgPaceWatt:  res.AvgPaceWatt,
		}
	}
	body, err := json.Marshal(RaceFinishedMessage{Type: "race_finished", Results: resultsJSON})
	if err == nil {
		select {
		case r.broadcast <- body:
		default:
			// Same non-blocking rule as broadcastSnapshot — a full or
			// nonexistent listener must never stall the actor.
		}
	}

	r.cancel()
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
