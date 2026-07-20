// Mirrors backend/internal/race/dtos.go

export interface CreateRaceResponse {
  id: string
  name: string
  distance_meters: number
  status: string
  created_by: string
  created_at: string
}

export interface JoinRaceResponse {
  race_id: string
  session_token: string
}

export interface StartRaceResponse {
  id: string
  status: string
  started_at: string
  prompt_text: string
}

export interface GetRaceTextResponse {
  prompt_text: string
}

export interface Participant {
  user_id: string
  display_name: string
  joined_at: string
}

export interface RaceStatusResponse {
  id: string
  name: string
  distance_meters: number
  status: string
  created_by: string
  participants: Participant[]
}
