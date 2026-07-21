import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api"
import { useRaceSocket } from "@/hooks/useRaceSocket"
import type { GetRaceTextResponse, RaceStatusResponse } from "@/types/race"
import { RaceScreenSidebar } from "@/components/race-screen/RaceScreenSidebar"
import { RaceTrack } from "@/components/race-screen/RaceTrack"

interface RaceScreenProps {
  raceId: string
  sessionToken: string | null
  raceDetail: RaceStatusResponse | null
  currentUserId: string | null
  promptText: string | null
  onPromptTextFetched: (text: string) => void
  onStarted: (promptText: string) => void
  onRefresh: () => void
  onLeftRace: () => void
}

// RaceScreen is the full-height 30/70 shell (race-screen.md) that takes
// over from the Dashboard state entirely once a race exists — RacesPage
// renders this OR the Dashboard, never both stacked. Owns useRaceSocket
// (moved up from the old TypingView) so both the sidebar leaderboard and
// the track panel share one raceState.
export function RaceScreen({
  raceId,
  sessionToken,
  raceDetail,
  currentUserId,
  promptText,
  onPromptTextFetched,
  onStarted,
  onRefresh,
  onLeftRace,
}: RaceScreenProps) {
  const [selectedVehicleId, setSelectedVehicleId] = useState<string | null>(null)
  const isActive = raceDetail?.status === "active"

  // useRaceSocket already no-ops on a null sessionToken — passing null
  // whenever the race isn't active yet preserves today's exact behavior of
  // never attempting a WS handshake against a room that hasn't been spawned
  // (Registry.Spawn only runs from POST /races/{id}/start).
  const { raceState, finished, connectionError, leaving, reconnecting, evicted, sendTelemetry, leaveRace } =
    useRaceSocket(raceId, isActive ? sessionToken : null)

  useEffect(() => {
    if (!isActive || promptText !== null) return
    apiFetch<GetRaceTextResponse>(`/races/${raceId}/text`)
      .then((res) => onPromptTextFetched(res.prompt_text))
      .catch(() => {
        /* surfaced via RaceScreenSidebar's connectionError fallback text */
      })
  }, [raceId, isActive, promptText, onPromptTextFetched])

  return (
    <div
      className="flex w-full gap-4 overflow-hidden rounded-2xl"
      style={{ height: "100vh", minHeight: "700px" }}
    >
      <div className="flex w-[30%] min-w-[340px] flex-col overflow-y-auto rounded-2xl border-2 bg-card p-5">
        <RaceScreenSidebar
          raceId={raceId}
          raceDetail={raceDetail}
          currentUserId={currentUserId}
          promptText={promptText}
          onStarted={onStarted}
          onRefresh={onRefresh}
          onLeftRace={onLeftRace}
          selectedVehicleId={selectedVehicleId}
          onSelectVehicle={setSelectedVehicleId}
          raceState={raceState}
          finished={finished}
          connectionError={connectionError}
          leaving={leaving}
          reconnecting={reconnecting}
          evicted={evicted}
          sendTelemetry={sendTelemetry}
          leaveRace={leaveRace}
        />
      </div>
      <div className="w-[70%]">
        {raceDetail && (
          <RaceTrack
            raceName={raceDetail.name}
            status={raceDetail.status}
            participants={raceDetail.participants}
            raceState={raceState}
            distanceMeters={raceDetail.distance_meters}
            currentUserId={currentUserId}
            localVehicleId={selectedVehicleId}
          />
        )}
      </div>
    </div>
  )
}
