package leaderboard

// LeaderboardMeResponse is GET /leaderboard/me's response body — also
// returned directly by LeaderboardService.GetMyStats, since it's already
// the exact shape the caller needs.
type LeaderboardMeResponse struct {
	RacesJoined int     `json:"races_joined"`
	RacesWon    int     `json:"races_won"`
	AvgWPM      float64 `json:"avg_wpm"`
}
