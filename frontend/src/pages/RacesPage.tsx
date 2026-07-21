import { useEffect } from "react"
import { useNavigate } from "react-router-dom"

import { isAuthenticated } from "@/lib/auth"
import { AppHeader } from "@/components/layout/AppHeader"
import { CreateRaceForm } from "@/components/races/CreateRaceForm"
import { JoinRaceForm } from "@/components/races/JoinRaceForm"
import { OpenRacesList } from "@/components/races/OpenRacesList"
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
    <div className="mx-auto flex max-w-5xl flex-col gap-6 p-6">
      <AppHeader />
      <StatCards />
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-6">
          <CreateRaceForm onCreated={handleEnterRace} />
          <JoinRaceForm onJoined={handleEnterRace} />
        </div>
        <OpenRacesList />
      </div>
    </div>
  )
}
