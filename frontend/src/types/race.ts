// Mirrors backend/internal/race/dtos.go

export interface CreateRaceResponse {
  id: string
  name: string
  distance_meters: number
  status: string
  created_by: string
  created_at: string
  // The creator is auto-added as a participant by CreateRace — this is
  // their own WS handshake credential, same shape as JoinRaceResponse's.
  session_token: string
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
  // null once the race is no longer pending (active, finished, or
  // cancelled) — mirrors pending-expiry.md's "nil means N/A" convention.
  pending_expires_at: string | null
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

// Broadcast the instant a room goes active (race-started-broadcast.md),
// reaching every connection already attached to the pending lobby.
export interface RaceStartedMessage {
  type: "race_started"
  prompt_text: string
}

// Broadcast when a pending room's lifetime runs out without the race
// starting (race-expired-broadcast.md) — no payload beyond the type
// discriminator, mirroring the backend's own message shape.
export interface RaceExpiredMessage {
  type: "race_expired"
}
