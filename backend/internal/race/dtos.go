package race

// Request/response DTOs for the race HTTP handlers, kept together so the
// wire format for this domain is easy to scan in one place.

type createRaceRequest struct {
	Name           string `json:"name"`
	DistanceMeters int    `json:"distance_meters"`
}

type createRaceResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DistanceMeters int    `json:"distance_meters"`
	Status         string `json:"status"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
	// SessionToken is the creator's own WS handshake credential — the
	// creator is auto-added as a participant by CreateRace, same as JoinRace.
	SessionToken string `json:"session_token"`
}

type joinRaceResponse struct {
	RaceID       string `json:"race_id"`
	SessionToken string `json:"session_token"`
}

type startRaceResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	PromptText string `json:"prompt_text"`
}

type getRaceTextResponse struct {
	PromptText string `json:"prompt_text"`
}

type participantResponse struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	JoinedAt    string `json:"joined_at"`
	// FinishRank/FinishTimeMs are nil until the race finishes
	// (race-detail-cold-visit.md) — same nullable shape as the live
	// race_finished WebSocket message's RaceResultJSON, so a cold REST
	// visit and a live connection can render results the same way.
	FinishRank   *int    `json:"finish_rank"`
	FinishTimeMs *int64  `json:"finish_time_ms"`
	AvgPaceWatt  float64 `json:"avg_pace_watt"`
}

type raceStatusResponse struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	DistanceMeters int                   `json:"distance_meters"`
	Status         string                `json:"status"`
	CreatedBy      string                `json:"created_by"`
	Participants   []participantResponse `json:"participants"`
	// PendingExpiresAt is nil once the race is no longer pending (active,
	// finished, or cancelled) — there's no expiry concept once a race has
	// actually started (room-lifecycle/pending-expiry.md).
	PendingExpiresAt *string `json:"pending_expires_at"`
}
