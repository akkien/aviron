package leaderboard

import (
	"net/http"
	"strconv"

	"github.com/akkien/aviron/internal/httpx"
	"github.com/akkien/aviron/internal/middleware"
)

type LeaderboardHandler struct {
	svc *LeaderboardService
}

func NewLeaderboardHandler(svc *LeaderboardService) *LeaderboardHandler {
	return &LeaderboardHandler{svc: svc}
}

// Me godoc
// @Summary Get my leaderboard stats
// @Description Returns the caller's own races-joined/races-won/avg-WPM stats. An account with no finished races yet gets all-zero stats, not a 404.
// @Tags leaderboard
// @Produce json
// @Success 200 {object} LeaderboardMeResponse
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Router /leaderboard/me [get]
func (h *LeaderboardHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	stats, err := h.svc.GetMyStats(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, stats)
}

// Top godoc
// @Summary Get the ranked leaderboard
// @Description Returns the top entries for the requested window, ranked by wins (avg WPM as tiebreaker).
// @Tags leaderboard
// @Produce json
// @Param window query string true "alltime or weekly"
// @Param limit query int false "max entries to return (default 20, capped at 100)"
// @Success 200 {object} LeaderboardTopResponse
// @Failure 400 {object} map[string]interface{} "field-keyed validation errors"
// @Failure 401 {object} map[string]string "error: unauthorized"
// @Router /leaderboard [get]
func (h *LeaderboardHandler) Top(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.UserIDFromContext(r.Context()); !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	windowParam := r.URL.Query().Get("window")
	// A missing/unparseable limit is treated as "not provided," not an
	// error — GetTop clamps a zero value to its own default.
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	resp, fieldErrs, err := h.svc.GetTop(r.Context(), windowParam, limit)
	if len(fieldErrs) > 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"errors": fieldErrs})
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, resp)
}
