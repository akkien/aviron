import { useCallback, useEffect, useState } from "react"
import { useNavigate } from "react-router-dom"

import { apiFetch } from "@/lib/api"
import { getUserID, isAuthenticated } from "@/lib/auth"
import type { RaceStatusResponse } from "@/types/race"
import { CreateRaceForm } from "@/components/races/CreateRaceForm"
import { JoinRaceForm } from "@/components/races/JoinRaceForm"
import { RaceStatusView } from "@/components/races/RaceStatusView"
import { TypingView } from "@/components/races/TypingView"

export function RacesPage() {
  const navigate = useNavigate()
  const [raceId, setRaceId] = useState<string | null>(null)
  const [raceDetail, setRaceDetail] = useState<RaceStatusResponse | null>(null)
  const [promptText, setPromptText] = useState<string | null>(null)
  // sessionToken is the WS handshake's credential (websocket-client.md) —
  // only set once the local player actually joins; the race's creator has
  // none until/unless they separately call Join too (a pre-existing Phase 1
  // gap: creating a race doesn't auto-add the creator as a participant).
  const [sessionToken, setSessionToken] = useState<string | null>(null)

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

  function handleCreated(id: string) {
    setPromptText(null)
    setSessionToken(null)
    setRaceId(id)
  }

  function handleJoined(id: string, token: string) {
    setPromptText(null)
    setSessionToken(token)
    setRaceId(id)
  }

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-6 p-6">
      <h1 className="text-2xl font-semibold">Races</h1>
      <div className="grid gap-6 sm:grid-cols-2">
        <CreateRaceForm onCreated={handleCreated} />
        <JoinRaceForm onJoined={handleJoined} />
      </div>

      {raceId && (
        <RaceStatusView
          raceId={raceId}
          raceDetail={raceDetail}
          currentUserId={getUserID()}
          onRefresh={() => refreshStatus(raceId)}
          onStarted={setPromptText}
        />
      )}

      {raceId && raceDetail?.status === "active" && (
        <TypingView
          raceId={raceId}
          sessionToken={sessionToken}
          distanceMeters={raceDetail.distance_meters}
          participants={raceDetail.participants}
          promptText={promptText}
          onPromptTextFetched={setPromptText}
        />
      )}
    </div>
  )
}
