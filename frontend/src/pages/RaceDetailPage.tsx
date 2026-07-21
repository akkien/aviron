import { useCallback, useEffect, useState } from "react"
import { Navigate, useLocation, useNavigate, useParams } from "react-router-dom"

import { apiFetch } from "@/lib/api"
import { getUserID, isAuthenticated } from "@/lib/auth"
import type { RaceStatusResponse } from "@/types/race"
import { RaceScreen } from "@/components/race-screen/RaceScreen"

interface RaceNavigationState {
  sessionToken?: string
}

// RaceDetailPage owns a race's own URL (/races/:raceId) — RacesPage used to
// hold raceId as local state, so a race had no URL of its own and quitting
// had nowhere concrete to send the player back to. sessionToken travels via
// router navigation state (set once, by RacesPage's create/join handlers)
// rather than being persisted anywhere — same lifetime it already had as
// RacesPage local state before this route existed; a raw page load/refresh
// at this URL still won't have a session_token, matching today's behavior
// (unchanged, not regressed by this change).
export function RaceDetailPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const { raceId } = useParams<{ raceId: string }>()
  const sessionToken = (location.state as RaceNavigationState | null)?.sessionToken ?? null

  const [raceDetail, setRaceDetail] = useState<RaceStatusResponse | null>(null)
  const [promptText, setPromptText] = useState<string | null>(null)

  useEffect(() => {
    if (!isAuthenticated()) navigate("/login")
  }, [navigate])

  const refreshStatus = useCallback((id: string) => {
    apiFetch<RaceStatusResponse>(`/races/${id}`)
      .then(setRaceDetail)
      .catch(() => setRaceDetail(null))
  }, [])

  useEffect(() => {
    if (raceId) refreshStatus(raceId)
  }, [raceId, refreshStatus])

  if (!raceId) {
    return <Navigate to="/races" replace />
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      <RaceScreen
        raceId={raceId}
        sessionToken={sessionToken}
        raceDetail={raceDetail}
        currentUserId={getUserID()}
        promptText={promptText}
        onPromptTextFetched={setPromptText}
        onStarted={setPromptText}
        onRefresh={() => refreshStatus(raceId)}
        onLeftRace={() => navigate("/races")}
      />
    </div>
  )
}
