import { useCallback, useEffect, useState } from "react"
import { Navigate, useLocation, useNavigate, useParams } from "react-router-dom"

import { apiFetch } from "@/lib/api"
import { getUserID, isAuthenticated } from "@/lib/auth"
import type { JoinRaceResponse, RaceStatusResponse } from "@/types/race"
import { RaceScreen } from "@/components/race-screen/RaceScreen"

interface RaceNavigationState {
  sessionToken?: string
}

// RaceDetailPage owns a race's own URL (/races/:raceId) — RacesPage used to
// hold raceId as local state, so a race had no URL of its own and quitting
// had nowhere concrete to send the player back to. sessionToken travels via
// router navigation state (set once, by RacesPage's create/join handlers)
// rather than being persisted anywhere — same lifetime it already had as
// RacesPage local state before this route existed.
//
// A raw page load/refresh at this URL still starts with no session_token
// (navigation state doesn't survive a reload) — but unlike before
// (idempotent-join.md), that's now recoverable rather than permanent:
// POST /races/{id}/join is idempotent for someone already a participant,
// so the recovery effect below silently re-joins to get a working token
// once raceDetail confirms the current user really is one, instead of
// leaving them stuck in read-only spectator mode.
export function RaceDetailPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const { raceId } = useParams<{ raceId: string }>()
  const [sessionToken, setSessionToken] = useState<string | null>(
    (location.state as RaceNavigationState | null)?.sessionToken ?? null,
  )

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

  // Best-effort session recovery: only fires while we don't already have a
  // token and raceDetail has confirmed the current user is a real
  // participant of a still-live (not finished/cancelled) race. A failure
  // just leaves the user in the existing read-only spectator fallback, not
  // a new error state. Re-fires on every subsequent raceDetail update (e.g.
  // from onRefresh) as long as sessionToken is still null, so recovery
  // isn't limited to one attempt at mount.
  useEffect(() => {
    if (sessionToken || !raceId || !raceDetail) return
    if (raceDetail.status === "finished" || raceDetail.status === "cancelled") return

    const userID = getUserID()
    const isParticipant = raceDetail.participants.some((p) => p.user_id === userID)
    if (!isParticipant) return

    apiFetch<JoinRaceResponse>(`/races/${raceId}/join`, { method: "POST" })
      .then((res) => setSessionToken(res.session_token))
      .catch(() => {
        /* stays in read-only spectator mode, same as today */
      })
  }, [sessionToken, raceId, raceDetail])

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
