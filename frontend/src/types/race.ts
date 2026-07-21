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

// Mirrors backend/internal/room's outbound WebSocket messages
// (websocket/protocol.md, race-completion/finish-race.md).

export interface ParticipantStateJSON {
  user_id: string
  distance_m: number
  rank: number
}

export interface RaceStateMessage {
  type: "race_state"
  tick: number
  participants: ParticipantStateJSON[]
}

// finish_rank/finish_time_ms are nullable: leave-race.md means a result
// entry doesn't always mean that participant finished — an evicted or
// quit participant shares one last-place finish_rank but has a null
// finish_time_ms (never reached the target).
export interface RaceResultJSON {
  user_id: string
  finish_rank: number | null
  finish_time_ms: number | null
  avg_pace_watt: number
}

export interface RaceFinishedMessage {
  type: "race_finished"
  results: RaceResultJSON[]
}
