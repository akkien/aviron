import { useEffect, useState } from "react"

import { laneColor } from "@/lib/colors"
import { apiFetch } from "@/lib/api"
import type { JoinRaceResponse, ListOpenRacesResponse, OpenRace } from "@/types/race"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

// How often this list re-fetches while mounted (open-races.md). This is a
// passive lobby-browsing widget on the Dashboard, not a race in progress —
// there's no WebSocket here and no real-time-correctness requirement, so a
// plain poll is enough: frequent enough that a new lobby or an updated join
// count feels reasonably live, infrequent enough not to hammer a plain REST
// endpoint for a decorative-adjacent widget.
const POLL_INTERVAL_MS = 5000

interface OpenRacesListProps {
  onJoined: (raceId: string, sessionToken: string) => void
}

export function OpenRacesList({ onJoined }: OpenRacesListProps) {
  const [races, setRaces] = useState<OpenRace[] | null>(null)
  const [joiningId, setJoiningId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    function fetchOpenRaces() {
      apiFetch<ListOpenRacesResponse>("/races")
        .then((res) => {
          if (!cancelled) setRaces(res.races)
        })
        .catch(() => {
          // A failed poll leaves the last-known list on screen rather than
          // clearing it — a transient network blip shouldn't make an
          // already-visible list disappear.
        })
    }

    fetchOpenRaces()
    const interval = setInterval(fetchOpenRaces, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [])

  async function handleJoin(raceId: string) {
    setError(null)
    setJoiningId(raceId)
    try {
      const res = await apiFetch<JoinRaceResponse>(`/races/${raceId}/join`, {
        method: "POST",
      })
      onJoined(raceId, res.session_token)
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to join race")
      setJoiningId(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Open Races</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {races === null && <p className="text-sm text-muted-foreground">Loading...</p>}
        {races !== null && races.length === 0 && (
          <p className="text-sm text-muted-foreground">No open races right now — create one!</p>
        )}
        {races?.map((race, i) => (
          <div
            key={race.id}
            className="flex items-center justify-between gap-3 rounded-md border bg-secondary/40 p-3"
          >
            <div className="flex items-center gap-3">
              <span
                className="h-3 w-3 shrink-0 rounded-full"
                style={{ backgroundColor: laneColor(i) }}
              />
              <div className="flex flex-col">
                <span className="text-sm font-medium">{race.name}</span>
                <span className="text-xs text-muted-foreground">
                  hosted by {race.host_display_name} · {race.distance_meters} words ·{" "}
                  {race.player_count}/{race.max_players} players
                </span>
              </div>
            </div>
            <Button
              size="sm"
              variant="secondary"
              disabled={joiningId === race.id}
              onClick={() => handleJoin(race.id)}
            >
              {joiningId === race.id ? "Joining..." : "Join"}
            </Button>
          </div>
        ))}
        {error && <p className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}
