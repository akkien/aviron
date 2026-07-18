package race

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/akkien/aviron/internal/httpx"
	"github.com/akkien/aviron/internal/middleware"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

type RaceHandler struct {
	svc *RaceService
}

func NewRaceHandler(svc *RaceService) *RaceHandler {
	return &RaceHandler{svc: svc}
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

	created, fieldErrs, err := h.svc.CreateRace(r.Context(), req.Name, req.DistanceMeters, userID)
	if len(fieldErrs) > 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, createRaceResponse{
		ID:             created.ID,
		Name:           created.Name,
		DistanceMeters: created.DistanceMeters,
		Status:         created.Status,
		CreatedBy:      created.CreatedBy,
		CreatedAt:      created.CreatedAt.Format(time.RFC3339),
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
// @Failure 409 {object} map[string]string "error: already_joined | race_not_pending"
// @Router /races/{id}/join [post]
func (h *RaceHandler) Join(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	raceID := r.PathValue("id")
	if !isValidUUID(raceID) {
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
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, joinRaceResponse{
		RaceID:       raceID,
		SessionToken: sessionToken,
	})
}
