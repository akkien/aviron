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
}

type joinRaceResponse struct {
	RaceID       string `json:"race_id"`
	SessionToken string `json:"session_token"`
}

type leaveRaceResponse struct {
	RaceID string `json:"race_id"`
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
}

type raceStatusResponse struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	DistanceMeters int                   `json:"distance_meters"`
	Status         string                `json:"status"`
	CreatedBy      string                `json:"created_by"`
	Participants   []participantResponse `json:"participants"`
}
