import { useEffect } from "react"
import { useNavigate } from "react-router-dom"

import { isAuthenticated } from "@/lib/auth"
import { AppHeader } from "@/components/layout/AppHeader"
import { CreateRaceForm } from "@/components/races/CreateRaceForm"
import { JoinRaceForm } from "@/components/races/JoinRaceForm"
import { OpenRacesList } from "@/components/races/OpenRacesList"
import { RankedLeaderboard } from "@/components/races/RankedLeaderboard"
import { StatCards } from "@/components/races/StatCards"

export function RacesPage() {
  const navigate = useNavigate()

  useEffect(() => {
    if (!isAuthenticated()) navigate("/login")
  }, [navigate])

  // Both create and join land the player on the same place: the race's own
  // URL (RaceDetailPage), handed its session_token via navigation state.
  function handleEnterRace(raceId: string, sessionToken: string) {
    navigate(`/races/${raceId}`, { state: { sessionToken } })
  }

  return (
    // h-screen + overflow-hidden on the root is what actually makes the
    // whole page fit without scrolling — AppHeader no longer needs
    // position:fixed to "stay in view," since nothing here scrolls in the
    // first place. Only OpenRacesList's own inner list is allowed to
    // scroll, within whatever height row 2 ends up being.
    <div className="flex h-screen flex-col overflow-hidden">
      <AppHeader />
      <div className="mx-auto flex w-full min-h-0 max-w-325 flex-1 flex-col gap-4 overflow-hidden px-10 py-2">
        {/* gap-4 here matches row 2's gap-4 below — both left columns are
            the same w-85, so equal gaps are what keep RankedLeaderboard's
            and OpenRacesList's left edges aligned. */}
        <div className="flex shrink-0 gap-4">
          <div className="w-85 shrink-0">
            <StatCards />
          </div>
          <div className="flex-1">
            <RankedLeaderboard />
          </div>
        </div>
        <div className="flex min-h-0 flex-1 items-stretch gap-4">
          <div className="flex w-85 shrink-0 flex-col gap-4">
            <CreateRaceForm onCreated={handleEnterRace} />
            <JoinRaceForm onJoined={handleEnterRace} />
          </div>
          <div className="min-w-105 flex-1">
            <OpenRacesList onJoined={handleEnterRace} />
          </div>
        </div>
      </div>
    </div>
  )
}
