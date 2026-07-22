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
// (Dashboard) and RaceDetailPage (this) are separate routes/pages, never
// both stacked. Owns useRaceSocket (moved up from the old TypingView) so
// both the sidebar leaderboard and the track panel share one raceState.
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

  // The socket now opens as soon as a session token exists at all
  // (live-lobby.md) — no longer gated on the race already being active.
  // Every player holds a live connection from the moment they land on this
  // page, pending or active, which is what lets race_started/race_expired
  // reach everyone the instant either happens. onStarted/onRefresh are
  // passed straight through so race_started can set promptText and
  // re-fetch REST status the same way handleStart already does for the
  // creator's own start action.
  const {
    raceState,
    finished,
    connectionError,
    leaving,
    reconnecting,
    evicted,
    expired,
    sendTelemetry,
    leaveRace,
  } = useRaceSocket(raceId, sessionToken, onStarted, onRefresh)

  // Quitting mid-race (the sidebar's "Quit Race" button, via leaveRace)
  // only ever flipped this local `leaving` flag — nothing told the caller
  // the player was done, so there was no route back except editing the URL
  // by hand. The pre-race "Leave" button (RaceScreenSidebar's own REST
  // call) already calls onLeftRace directly; this makes the mid-race path
  // do the same.
  useEffect(() => {
    if (leaving) onLeftRace()
  }, [leaving, onLeftRace])

  // race_expired reaches every still-connected player the instant a
  // pending room's lifetime runs out — same redirect-on-quit pattern as
  // `leaving` above, reusing onLeftRace rather than a second redirect path
  // (live-lobby.md).
  useEffect(() => {
    if (expired) onLeftRace()
  }, [expired, onLeftRace])

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
          selectedVehicleId={selectedVehicleId}
          onSelectVehicle={setSelectedVehicleId}
          raceState={raceState}
          finished={finished}
          connectionError={connectionError}
          leaving={leaving}
          reconnecting={reconnecting}
          evicted={evicted}
          expired={expired}
          sendTelemetry={sendTelemetry}
          leaveRace={leaveRace}
        />
      </div>
      <div className="w-[70%]">
        {raceDetail && (
          <RaceTrack
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
