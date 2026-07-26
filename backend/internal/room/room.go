package room

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"
)

// gracePeriodDuration is how long a disconnected participant's state is
// kept before they're removed for good (reconnection/grace-period.md). A
// var, not a const, so tests can shorten it instead of sleeping the real
// 30s — see TestRoomActor_Run_GracePeriodExpiry_RemovesParticipant.
var gracePeriodDuration = 30 * time.Second

type ParticipantState struct {
	UserID       string
	DisplayName  string
	WordsCorrect int
	LastSeq      int
	// PaceWatt is the latest self-reported average WPM (project-overview.md
	// §13 — "pace_watt" is a holdover field name from the original
	// fitness-telemetry design), updated on every TelemetryReceived. Since
	// the client computes it as a cumulative average from race start, the
	// value at finish time already is the participant's final average WPM.
	PaceWatt          float64
	ConnectedAt       time.Time
	DisconnectedAt    *time.Time
	DisconnectedCount int
	// graceTimer fires ParticipantEvicted if this participant doesn't reconnect
	// in time. Stopped and cleared on reconnect so a stale expiry can never
	// remove someone who's since reattached.
	graceTimer *time.Timer
	// FinishedAt/FinishRank are set together, once, the moment WordsCorrect
	// reaches the room's distanceMeters (race-completion/finish-race.md).
	FinishedAt *time.Time
	FinishRank *int
}

// TickObserver receives the wall-clock duration of a single
// broadcastSnapshot() call, once per tick (prometheus-metrics.md). Defined
// here, next to RoomActor, so internal/metrics can depend on internal/room
// without internal/room ever needing to import internal/metrics —
// *metrics.Metrics satisfies this structurally, the same one-directional
// shape as RaceFinisher/RaceLeaver/RaceCanceller (finish.go).
type TickObserver interface {
	ObserveTick(d time.Duration)
}

type RoomActor struct {
	id           string
	participants map[string]*ParticipantState
	// evicted tracks user_ids removed via grace-period expiry or an
	// intentional quit (leave-race.md), so a too-late reconnect attempt can
	// be rejected by websocket/ws-endpoint.md instead of silently rejoining
	// as a fresh participant.
	evicted map[string]struct{}
	// departedParticipants holds anyone removed from participants — via
	// ParticipantEvicted or ParticipantLeft — before finishing, so
	// checkRaceFinished can still produce a result for them (leave-race.md)
	// instead of the entry silently vanishing the moment they're removed.
	departedParticipants map[string]*ParticipantState
	// totalParticipants counts every genuinely new participant this room
	// ever saw (leave-race.md) — incremented only in ParticipantJoined's
	// unknown-participant branch, never on a reconnect or a duplicate join
	// while still connected. Used as the shared rank for anyone who leaves
	// without finishing.
	totalParticipants int
	distanceMeters    int
	// active is true once the race has actually started (MarkActive) — lets
	// checkRaceFinished tell "never started" apart from "started, then
	// emptied out" (see there).
	active    bool
	tickCount int64
	inbox     chan RoomEvent
	broadcast chan []byte
	ctx       context.Context
	cancel    context.CancelFunc
	// finisher persists final results once the race completes
	// (race-completion/finish-race.md) — see finish.go.
	finisher RaceFinisher
	// leaver persists a pending-race participant intentionally leaving
	// (pending-connections.md) — see finish.go.
	leaver RaceLeaver
	// canceller persists a pending race's cancellation once the room tears
	// down before ever going active (room-lifecycle/cancelled-race-status.md)
	// — see finish.go.
	canceller RaceCanceller
	// publisher publishes workout.sample/race.finished events to Kafka
	// (kafka-producer.md) — see publisher.go.
	publisher EventPublisher
	// startedAt is this actor's own construction time, used as the baseline
	// for FinishTimeMs. Not literally races.started_at (that UPDATE and this
	// Spawn happen milliseconds apart, in the same handler) — close enough
	// for a side project, not worth threading the DB timestamp through.
	startedAt time.Time
	// finished guards against calling finisher.FinishRace more than once —
	// belt-and-suspenders on top of checkRaceFinished's own event-transition
	// guards (see there for why a double-call shouldn't be reachable anyway).
	finished bool
	// finishedCount is a monotonic count of how many participants have
	// finished so far, used to assign FinishRank in finishing order. Not
	// derived by counting FinishRank != nil over r.participants at the
	// moment someone finishes: a participant who already finished can later
	// depart (their connection drops and the grace period lapses, or they
	// send leave_race after already crossing the line) and move into
	// departedParticipants, which would silently drop them from that count
	// and hand the next finisher a duplicate rank (leave-race.md). A counter
	// untouched by departure sidesteps that entirely.
	finishedCount int
	// logger is pre-tagged with this room's race_id (Registry.Spawn does
	// the tagging, once, before construction) so call sites below don't
	// need to repeat it.
	logger *slog.Logger
	// tickObserver receives each tick's broadcastSnapshot() duration
	// (prometheus-metrics.md) — see TickObserver.
	tickObserver TickObserver
}

// NewRoomActor constructs a room actor for race id, seeded with the race
// row's distanceMeters. A room actor exists for a race's entire lifetime,
// spawned at creation, before prompt_text exists — see MarkActive for how
// the actor later learns the race has actually started. Call go actor.Run()
// to start it — spawning and tracking instances is room-registry.md's job,
// not this constructor's.
func NewRoomActor(ctx context.Context, id string, distanceMeters int, broadcast chan []byte, finisher RaceFinisher, leaver RaceLeaver, canceller RaceCanceller, publisher EventPublisher, logger *slog.Logger, tickObserver TickObserver) *RoomActor {
	actorCtx, cancel := context.WithCancel(ctx)
	r := &RoomActor{
		id:                   id,
		participants:         make(map[string]*ParticipantState),
		evicted:              make(map[string]struct{}),
		departedParticipants: make(map[string]*ParticipantState),
		distanceMeters:       distanceMeters,
		inbox:                make(chan RoomEvent, 64), // generous buffer: a burst of telemetry must not block reader goroutines
		broadcast:            broadcast,
		ctx:                  actorCtx,
		cancel:               cancel,
		finisher:             finisher,
		leaver:               leaver,
		canceller:            canceller,
		publisher:            publisher,
		startedAt:            time.Now(),
		logger:               logger,
		tickObserver:         tickObserver,
	}
	time.AfterFunc(noShowTimeoutDuration, func() {
		r.Send(noShowTimeout{})
	})
	time.AfterFunc(PendingTimeoutDuration, func() {
		r.Send(pendingExpired{})
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

// InboxLen and BroadcastLen report how many messages are currently queued
// in this room's inbox/broadcast channels (prometheus-metrics.md). len(ch)
// is documented-safe to call from any goroutine, so these need no
// synchronization beyond what the channels themselves already provide.
func (r *RoomActor) InboxLen() int {
	return len(r.inbox)
}

func (r *RoomActor) BroadcastLen() int {
	return len(r.broadcast)
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

// activated carries the race's prompt text so applyEvent can broadcast
// race_started (websocket/race-started-broadcast.md) — MarkActive is the
// only place that sends it. Event-through-inbox rather than MarkActive
// mutating r.active directly, consistent with every other piece of room
// state (room-actor-core.md's single-writer principle).
type activated struct {
	PromptText string
}

func (activated) isRoomEvent() {}

// MarkActive tells the room actor its race has started and broadcasts
// race_started to every already-attached connection. Called by
// RaceHandler.Start once RaceService.StartRace succeeds.
func (r *RoomActor) MarkActive(promptText string) {
	r.Send(activated{PromptText: promptText})
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
			start := time.Now()
			r.broadcastSnapshot()
			r.tickObserver.ObserveTick(time.Since(start))
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
			// Only a genuinely new participant counts — not a reconnect, not a
			// duplicate join while still connected (leave-race.md's shared
			// last-place rank is "how many distinct people ever raced here").
			r.totalParticipants++
		}
		// Broadcast immediately rather than waiting for the next tick (up to
		// 250ms away) — websocket/protocol.md's join_race message is meant to
		// get the newly-attached client a snapshot right away, not leave it
		// looking at nothing until the ticker fires. Also doubles as the
		// reconnecting client's own resync, since it's already registered
		// with the room's broadcast fan-out by the time this event applies.
		r.broadcastSnapshot()
	case TelemetryReceived:
		if !r.active {
			return // race hasn't started yet — nothing to accumulate progress against
		}
		p, ok := r.participants[e.UserID]
		if !ok || e.Seq <= p.LastSeq {
			return // unknown participant, or stale/duplicate/out-of-order — drop silently
		}
		p.WordsCorrect = e.WordsCorrect
		p.LastSeq = e.Seq
		p.PaceWatt = e.PaceWatt
		if err := r.publisher.PublishWorkoutSample(r.ctx, r.id, e.UserID, time.Now(), p.WordsCorrect, p.PaceWatt); err != nil {
			// Log-and-continue, no retry — same precedent as finishRace's
			// own Postgres write below: nothing downstream of Kafka is
			// this project's system of record.
			r.logger.Error("publish workout sample failed", slog.Any("error", err))
		}
		if p.FinishRank == nil && p.WordsCorrect >= r.distanceMeters {
			// race-completion/finish-race.md: a participant individually
			// "finishes" the moment they reach the target, in finishing
			// order — first to reach it is rank 1. r.finishedCount (not a
			// live count of FinishRank != nil over r.participants) so a
			// finisher who's since departed still counts toward the next
			// finisher's rank instead of leaving a gap a later arrival could
			// collide with (leave-race.md).
			now := time.Now()
			p.FinishedAt = &now
			r.finishedCount++
			rank := r.finishedCount
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
			r.Send(ParticipantEvicted{UserID: userID})
		})
	case ParticipantEvicted:
		// Guard against a reconnect and an already-fired timer racing each
		// other: only honor this if the participant is still the one who
		// disconnected (DisconnectedAt != nil). If they reconnected in the
		// gap between the timer firing and this event being applied, the
		// ParticipantJoined case above already cleared DisconnectedAt and
		// stopped the timer — this stale event must not remove them anyway.
		if p, ok := r.participants[e.UserID]; ok && p.DisconnectedAt != nil {
			r.departParticipant(e.UserID, p)
		}
	case ParticipantLeft:
		// An intentional quit (leave-race.md): unlike ParticipantEvicted,
		// unconditional — a still-connected participant sending this is
		// exactly who's expected to, so there's no DisconnectedAt guard to
		// check. Removed immediately, no timer, no grace period.
		if p, ok := r.participants[e.UserID]; ok {
			if r.active {
				r.departParticipant(e.UserID, p)
			} else {
				// Leaving before the race started (pending-connections.md):
				// no result to preserve, and unlike a mid-race quit this
				// person should be free to join again — remove outright,
				// no departedParticipants, no evicted. Removed before the
				// Postgres call, not after: the reader goroutine closes this
				// connection right after sending leave_race, so unlike a
				// real disconnect no grace-period path will ever run later
				// to clean up a participant left in place by a failed call.
				delete(r.participants, e.UserID)
				if err := r.leaver.LeaveRace(r.ctx, r.id, e.UserID); err != nil {
					r.logger.Error("leave race failed", slog.String("user_id", e.UserID), slog.Any("error", err))
				}
				// If that was the last participant, cancel the race now
				// instead of leaving it pending until PendingTimeoutDuration
				// elapses on its own — checkRaceFinished's !r.active branch
				// already handles "room emptied out before starting" via
				// expirePendingRoom, but nothing called it from this path
				// before (unlike departParticipant, used by the active-race
				// leave above).
				r.checkRaceFinished()
			}
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
	case activated:
		r.active = true
		r.broadcastRaceStarted(e.PromptText)
	case pendingExpired:
		// A no-op if the race already started (r.active) or the room has
		// already torn down for some other reason (r.finished) — the timer
		// firing concurrently with a real start must never tear down a race
		// that's actually running.
		if r.active || r.finished {
			return
		}
		r.expirePendingRoom()
	}
}

// broadcastRaceStarted sends race_started to every connection already
// attached to this room (websocket/race-started-broadcast.md) — same
// marshal-then-non-blocking-send shape finishRace already uses for
// race_finished, riding the existing hub fan-out with no new delivery path.
func (r *RoomActor) broadcastRaceStarted(promptText string) {
	body, err := json.Marshal(RaceStartedMessage{Type: "race_started", PromptText: promptText})
	if err != nil {
		return // can't happen with these field types, but must not panic the actor loop
	}
	select {
	case r.broadcast <- body:
	default:
		// Same non-blocking rule as broadcastSnapshot/finishRace — a full or
		// nonexistent listener must never stall the actor.
	}
}

// expirePendingRoom tears down a room that's still pending — either because
// PendingTimeoutDuration elapsed (pendingExpired) or because it emptied out
// without ever starting (checkRaceFinished's noShowTimeout/departed-to-empty
// paths, room-lifecycle/pending-expiry.md). Broadcasting race_expired from
// both callers is safe, not just convenient: by the time the empty-room path
// reaches this method, r.participants is provably empty (unlike active telemetry,
// nothing while pending can ever set a FinishRank), and since pending
// participants are exactly the room's live WebSocket connections
// (pending-connections.md), an empty pending room already has nobody
// attached to hear it anyway.
//
// Persists races.status = 'cancelled' before broadcasting or tearing down
// (room-lifecycle/cancelled-race-status.md) — mirrors finishRace's own
// persist-before-notify order exactly: on failure, log and return without
// broadcasting or cancelling, so the room stays running rather than
// silently vanishing on a Postgres hiccup, the same no-retry gap finishRace
// already accepts.
func (r *RoomActor) expirePendingRoom() {
	if err := r.canceller.CancelRace(r.ctx, r.id); err != nil {
		r.logger.Error("cancel race failed", slog.Any("error", err))
		return
	}
	r.broadcastRaceExpired()
	r.finished = true
	r.cancel()
}

// broadcastRaceExpired sends race_expired to every connection still attached
// to this room, before it's torn down — same marshal-then-non-blocking-send
// shape broadcastRaceStarted/finishRace already use, riding the existing hub
// fan-out with no new delivery path. Called before r.cancel() in
// expirePendingRoom, relying on the same hub-drains-before-done/writeLoop-
// drains-off-hub.closed guarantee already proven for race_finished.
func (r *RoomActor) broadcastRaceExpired() {
	body, err := json.Marshal(RaceExpiredMessage{Type: "race_expired"})
	if err != nil {
		return // can't happen with this field type, but must not panic the actor loop
	}
	select {
	case r.broadcast <- body:
	default:
		// Same non-blocking rule as every other broadcast site — a full or
		// nonexistent listener must never stall the actor.
	}
}

// departParticipant moves a participant out of the live participants map and
// into departedParticipants (leave-race.md), so checkRaceFinished can still
// produce a result for them instead of them vanishing from the finish
// transaction entirely. Shared by both ParticipantEvicted and ParticipantLeft
// — the two events differ only in whether a guard gates removal, not in what
// removal itself does.
func (r *RoomActor) departParticipant(userID string, p *ParticipantState) {
	delete(r.participants, userID)
	r.departedParticipants[userID] = p
	r.evicted[userID] = struct{}{}
	r.checkRaceFinished()
}

// checkRaceFinished is race-completion/finish-race.md's finish condition:
// the race is over once there are zero live participants remaining, either
// because everyone who connected eventually left (grace period expired or
// quit for each) or nobody finished (participants is empty). Called from
// every event that could produce that transition — TelemetryReceived (someone
// just finished), departParticipant (someone just got evicted or quit), and
// noShowTimeout (nobody ever joined) — never from
// ParticipantJoined/ParticipantDisconnected/evictionQuery, none of which can
// complete a race by themselves.
//
// Results union participants (everyone still live — by this point, always
// finishers, since a live non-finisher would have returned above) with
// departedParticipants (leave-race.md: everyone evicted or who quit before
// finishing). buildParticipantResult assigns the latter group's shared
// last-place rank.
func (r *RoomActor) checkRaceFinished() {
	if r.finished {
		return
	}
	for _, p := range r.participants {
		if p.FinishRank == nil {
			return // still someone connected-and-racing or in their grace period
		}
	}

	if !r.active {
		// The room emptied out (or nobody ever showed up, noShowTimeout)
		// without the race ever actually starting — there's no real race
		// to persist: no started_at, no participant who actually raced.
		// Tear down with zero Postgres writes rather than calling
		// finisher.FinishRace with an empty or meaningless result set.
		r.expirePendingRoom()
		return
	}

	results := make([]ParticipantResult, 0, len(r.participants)+len(r.departedParticipants))
	for _, p := range r.participants {
		results = append(results, r.buildParticipantResult(p))
	}
	for _, p := range r.departedParticipants {
		results = append(results, r.buildParticipantResult(p))
	}
	r.finishRace(results)
}

// buildParticipantResult converts one participant's in-memory state into its
// final race_participants row. A participant who never finished (evicted or
// quit, leave-race.md) has no FinishRank yet at this point — they share one
// rank with every other non-finisher, equal to totalParticipants (the total
// number of distinct participants this room ever saw), not finishing order
// and not a per-quitter sequential rank. FinishTimeMs stays nil for them
// (never set), matching ParticipantResult's documented "nil means never
// finished" contract — unlike the previous version of this code, which
// always wrote a non-nil *int64 (zero for anyone reaching this loop without
// FinishedAt set); that never surfaced as a bug before this feature, since a
// live non-finisher always blocked the finish-condition loop above from ever
// reaching a non-finisher in the first place.
func (r *RoomActor) buildParticipantResult(p *ParticipantState) ParticipantResult {
	rank := p.FinishRank
	if rank == nil {
		nonFinisherRank := r.totalParticipants
		rank = &nonFinisherRank
	}
	var finishTimeMs *int64
	if p.FinishedAt != nil {
		ms := p.FinishedAt.Sub(r.startedAt).Milliseconds()
		finishTimeMs = &ms
	}
	return ParticipantResult{
		UserID:       p.UserID,
		FinishRank:   rank,
		FinishTimeMs: finishTimeMs,
		// p.PaceWatt is the client's own cumulative-average WPM as of its
		// last telemetry message, which for a finisher is also their final
		// average WPM for the whole race (see ParticipantState.PaceWatt).
		AvgPaceWatt:       p.PaceWatt,
		DisconnectedCount: p.DisconnectedCount,
	}
}

// finishRace persists results, notifies clients, and tears the room down.
// Called only from checkRaceFinished, always from inside applyEvent — the
// single-writer goroutine — so this blocks Run()'s select loop for the
// duration of the Postgres write, which is fine: a finishing race has
// nothing left to process concurrently anyway.
func (r *RoomActor) finishRace(results []ParticipantResult) {
	// The telemetry that just triggered this finish (e.g. the last
	// participant reaching distanceMeters) is only reflected in
	// r.participants' in-memory state so far — the next periodic
	// broadcastSnapshot tick (up to 250ms away) never gets a chance to run,
	// since finishing happens synchronously inside the same applyEvent call.
	// Without this, a client's own vehicle would visually freeze wherever
	// the last tick left it and jump straight to the results screen,
	// instead of ever being seen reaching the finish line.
	r.broadcastSnapshot()

	if err := r.finisher.FinishRace(r.ctx, r.id, r.distanceMeters, results); err != nil {
		// No retry: this is a side project's accepted gap, not a production
		// resilience story. Logged and left running rather than torn down,
		// so at least the room doesn't silently vanish if Postgres hiccups.
		r.logger.Error("finish race failed", slog.Any("error", err))
		return
	}
	r.finished = true

	if err := r.publisher.PublishRaceFinished(r.ctx, r.id, results); err != nil {
		// Same log-and-continue, no-retry precedent as the Postgres write
		// above — a dropped Kafka publish is strictly less severe, since
		// Postgres (not Kafka) is this project's system of record.
		r.logger.Error("publish race finished failed", slog.Any("error", err))
	}

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
