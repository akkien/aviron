import { useCallback, useEffect, useState } from "react"
import { useNavigate } from "react-router-dom"

import { apiFetch } from "@/lib/api"
import { getUserID, isAuthenticated } from "@/lib/auth"
import type { RaceStatusResponse } from "@/types/race"
import { AppHeader } from "@/components/layout/AppHeader"
import { CreateRaceForm } from "@/components/races/CreateRaceForm"
import { JoinRaceForm } from "@/components/races/JoinRaceForm"
import { OpenRacesList } from "@/components/races/OpenRacesList"
import { StatCards } from "@/components/races/StatCards"
import { RaceScreen } from "@/components/race-screen/RaceScreen"

export function RacesPage() {
  const navigate = useNavigate()
  const [raceId, setRaceId] = useState<string | null>(null)
  const [raceDetail, setRaceDetail] = useState<RaceStatusResponse | null>(null)
  const [promptText, setPromptText] = useState<string | null>(null)
  // sessionToken is the WS handshake's credential (websocket-client.md) —
  // set on both create and join now that CreateRace auto-adds the creator
  // as a participant and returns their own session_token too.
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

  function handleCreated(id: string, token: string) {
    setPromptText(null)
    setSessionToken(token)
    setRaceId(id)
  }

  function handleJoined(id: string, token: string) {
    setPromptText(null)
    setSessionToken(token)
    setRaceId(id)
  }

  function handleLeftRace() {
    setRaceId(null)
    setRaceDetail(null)
    setPromptText(null)
    setSessionToken(null)
  }

  if (raceId) {
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
          onLeftRace={handleLeftRace}
        />
      </div>
    )
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6 p-6">
      <AppHeader />
      <StatCards />
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-6">
          <CreateRaceForm onCreated={handleCreated} />
          <JoinRaceForm onJoined={handleJoined} />
        </div>
        <OpenRacesList />
      </div>
    </div>
  )
}
