package race

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/akkien/aviron/internal/httpx"
	"github.com/akkien/aviron/internal/middleware"
)

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
