package leaderboard

// LeaderboardMeResponse is GET /leaderboard/me's response body — also
// returned directly by LeaderboardService.GetMyStats, since it's already
// the exact shape the caller needs.
type LeaderboardMeResponse struct {
	RacesJoined int     `json:"races_joined"`
	RacesWon    int     `json:"races_won"`
	AvgWPM      float64 `json:"avg_wpm"`
}

// LeaderboardEntryResponse is one ranked row in GET /leaderboard's
// response. Field names deliberately reuse LeaderboardMeResponse's
// established vocabulary (races/wins/avg_wpm are the same metrics, just
// for another user) rather than inventing new naming for the same data.
type LeaderboardEntryResponse struct {
	Rank        int     `json:"rank"`
	UserID      string  `json:"user_id"`
	DisplayName string  `json:"display_name"`
	Races       int     `json:"races"`
	Wins        int     `json:"wins"`
	AvgWPM      float64 `json:"avg_wpm"`
}

// LeaderboardTopResponse is GET /leaderboard's response body — the
// requested window is echoed back so a client doesn't have to remember
// what it asked for.
type LeaderboardTopResponse struct {
	Window  string                     `json:"window"`
	Entries []LeaderboardEntryResponse `json:"entries"`
}
