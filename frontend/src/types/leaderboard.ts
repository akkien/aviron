// Mirrors backend/internal/leaderboard/dtos.go

export interface LeaderboardMeResponse {
  races_joined: number
  races_won: number
  avg_wpm: number
}

export interface LeaderboardEntry {
  rank: number
  user_id: string
  display_name: string
  races: number
  wins: number
  avg_wpm: number
}

export interface LeaderboardTopResponse {
  window: "alltime" | "weekly"
  page: number
  total_pages: number
  entries: LeaderboardEntry[]
}
