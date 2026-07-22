package leaderboard

import (
	"net/http"

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
