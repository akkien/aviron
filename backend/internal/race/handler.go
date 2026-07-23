package race

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/akkien/aviron/internal/httpx"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/room"
)

// raceIDPattern matches GenerateRaceID's shape (id.go): 12 base58 characters
// (no 0/O/I/l, to match the same alphabet a race id is actually generated
// from).
var raceIDPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{12}$`)

func isValidRaceID(s string) bool {
	return raceIDPattern.MatchString(s)
}

type RaceHandler struct {
	svc      *RaceService
	registry *room.Registry
	// ctx is the process's root context, used (not r.Context()) when spawning
	// a room actor so it keeps running after the triggering HTTP request
	// returns and its own context is cancelled.
	ctx    context.Context
	logger *slog.Logger
}

func NewRaceHandler(svc *RaceService, registry *room.Registry, ctx context.Context, logger *slog.Logger) *RaceHandler {
	return &RaceHandler{svc: svc, registry: registry, ctx: ctx, logger: logger}
}

// Create godoc
// @Summary Create a race
// @Description Creates a new typing race with a name and target word count
// @Tags races
// @Accept json
// @Produce json
// @Param request body createRaceRequest true "Create race payload"
// @Success 201 {object} createRaceResponse
// @Failure 400 {object} map[string]interface{} "field-keyed validation errors"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Router /races [post]
func (h *RaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req createRaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	created, sessionToken, fieldErrs, err := h.svc.CreateRace(r.Context(), req.Name, req.DistanceMeters, userID)
	if len(fieldErrs) > 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	h.registry.Spawn(h.ctx, created.ID, created.DistanceMeters, h.svc, h.svc, h.svc)

	httpx.WriteJSON(w, http.StatusCreated, createRaceResponse{
		ID:             created.ID,
		Name:           created.Name,
		DistanceMeters: created.DistanceMeters,
		Status:         created.Status,
		CreatedBy:      created.CreatedBy,
		CreatedAt:      created.CreatedAt.Format(time.RFC3339),
		SessionToken:   sessionToken,
	})
}

// Join godoc
// @Summary Join a race
// @Description Joins an existing race as a participant, returning a per-race session token
// @Tags races
// @Produce json
// @Param id path string true "Race ID"
// @Success 200 {object} joinRaceResponse
// @Failure 400 {object} map[string]string "error: invalid_race_id"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Failure 404 {object} map[string]string "error: race_not_found"
// @Failure 409 {object} map[string]string "error: already_joined | race_not_pending | race_full"
// @Router /races/{id}/join [post]
func (h *RaceHandler) Join(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	raceID := r.PathValue("id")
	if !isValidRaceID(raceID) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_race_id")
		return
	}

	sessionToken, err := h.svc.JoinRace(r.Context(), raceID, userID)
	switch {
	case errors.Is(err, ErrRaceNotFound):
		httpx.WriteError(w, http.StatusNotFound, "race_not_found")
		return
	case errors.Is(err, ErrRaceNotPending):
		httpx.WriteError(w, http.StatusConflict, "race_not_pending")
		return
	case errors.Is(err, ErrAlreadyJoined):
		httpx.WriteError(w, http.StatusConflict, "already_joined")
		return
	case errors.Is(err, ErrRaceFull):
		httpx.WriteError(w, http.StatusConflict, "race_full")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, joinRaceResponse{
		RaceID:       raceID,
		SessionToken: sessionToken,
	})
}

// Start godoc
// @Summary Start a race
// @Description Creator starts the race: generates the shared prompt text and flips status to active
// @Tags races
// @Produce json
// @Param id path string true "Race ID"
// @Success 200 {object} startRaceResponse
// @Failure 400 {object} map[string]string "error: invalid_race_id"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Failure 403 {object} map[string]string "error: forbidden"
// @Failure 404 {object} map[string]string "error: race_not_found"
// @Failure 409 {object} map[string]string "error: race_not_pending"
// @Router /races/{id}/start [post]
func (h *RaceHandler) Start(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	raceID := r.PathValue("id")
	if !isValidRaceID(raceID) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_race_id")
		return
	}

	started, err := h.svc.StartRace(r.Context(), raceID, userID)
	switch {
	case errors.Is(err, ErrRaceNotFound):
		httpx.WriteError(w, http.StatusNotFound, "race_not_found")
		return
	case errors.Is(err, ErrNotCreator):
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	case errors.Is(err, ErrRaceNotPending):
		httpx.WriteError(w, http.StatusConflict, "race_not_pending")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	promptText := ""
	if started.PromptText != nil {
		promptText = *started.PromptText
	}
	startedAt := ""
	if started.StartedAt != nil {
		startedAt = started.StartedAt.Format(time.RFC3339)
	}

	actor, ok := h.registry.Get(raceID)
	if !ok {
		h.logger.Error("room actor missing at start", slog.String("race_id", raceID))
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	actor.MarkActive(promptText)

	httpx.WriteJSON(w, http.StatusOK, startRaceResponse{
		ID:         started.ID,
		Status:     started.Status,
		StartedAt:  startedAt,
		PromptText: promptText,
	})
}

// Text godoc
// @Summary Get race prompt text
// @Description Fetches the race's already-generated prompt text
// @Tags races
// @Produce json
// @Param id path string true "Race ID"
// @Success 200 {object} getRaceTextResponse
// @Failure 400 {object} map[string]string "error: invalid_race_id"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Failure 404 {object} map[string]string "error: race_not_found"
// @Failure 409 {object} map[string]string "error: prompt_not_ready"
// @Router /races/{id}/text [get]
func (h *RaceHandler) Text(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	raceID := r.PathValue("id")
	if !isValidRaceID(raceID) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_race_id")
		return
	}

	promptText, err := h.svc.GetRaceText(r.Context(), raceID)
	switch {
	case errors.Is(err, ErrRaceNotFound):
		httpx.WriteError(w, http.StatusNotFound, "race_not_found")
		return
	case errors.Is(err, ErrPromptNotReady):
		httpx.WriteError(w, http.StatusConflict, "prompt_not_ready")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, getRaceTextResponse{PromptText: promptText})
}

// ListOpen godoc
// @Summary List open races
// @Description Returns pending, joinable races the caller hasn't already created or joined
// @Tags races
// @Produce json
// @Success 200 {object} listOpenRacesResponse
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Router /races [get]
func (h *RaceHandler) ListOpen(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	open, err := h.svc.ListOpenRaces(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	races := make([]openRaceResponse, len(open))
	for i, o := range open {
		races[i] = openRaceResponse{
			ID:              o.ID,
			Name:            o.Name,
			DistanceMeters:  o.DistanceMeters,
			HostDisplayName: o.HostDisplayName,
			PlayerCount:     o.PlayerCount,
			MaxPlayers:      o.MaxPlayers,
			CreatedAt:       o.CreatedAt.Format(time.RFC3339),
		}
	}

	httpx.WriteJSON(w, http.StatusOK, listOpenRacesResponse{Races: races})
}

// Status godoc
// @Summary Get race status
// @Description Returns a race's current status and participant list
// @Tags races
// @Produce json
// @Param id path string true "Race ID"
// @Success 200 {object} raceStatusResponse
// @Failure 400 {object} map[string]string "error: invalid_race_id"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Failure 404 {object} map[string]string "error: race_not_found"
// @Router /races/{id} [get]
func (h *RaceHandler) Status(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	raceID := r.PathValue("id")
	if !isValidRaceID(raceID) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_race_id")
		return
	}

	detail, err := h.svc.GetRaceDetail(r.Context(), raceID)
	switch {
	case errors.Is(err, ErrRaceNotFound):
		httpx.WriteError(w, http.StatusNotFound, "race_not_found")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	participants := make([]participantResponse, len(detail.Participants))
	for i, p := range detail.Participants {
		participants[i] = participantResponse{
			UserID:       p.UserID,
			DisplayName:  p.DisplayName,
			JoinedAt:     p.JoinedAt.Format(time.RFC3339),
			FinishRank:   p.FinishRank,
			FinishTimeMs: p.FinishTimeMs,
			AvgPaceWatt:  p.AvgPaceWatt,
		}
	}

	var pendingExpiresAt *string
	if detail.Status == "pending" {
		formatted := detail.CreatedAt.Add(room.PendingTimeoutDuration).Format(time.RFC3339)
		pendingExpiresAt = &formatted
	}

	httpx.WriteJSON(w, http.StatusOK, raceStatusResponse{
		ID:               detail.ID,
		Name:             detail.Name,
		DistanceMeters:   detail.DistanceMeters,
		Status:           detail.Status,
		CreatedBy:        detail.CreatedBy,
		Participants:     participants,
		PendingExpiresAt: pendingExpiresAt,
	})
}
