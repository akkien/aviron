import { useState } from "react"

import { laneColor } from "@/lib/colors"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

interface SeedRace {
  id: string
  name: string
  host: string
  wordCount: number
  players: number
  maxPlayers: number
  laneColorIndex: number
}

// Hardcoded seed list — there is no GET /races browsable-list endpoint.
// Purely decorative: clicking "Join" only increments the row's local player
// count client-side (capped at max), it never calls the real join API. The
// actual way to join a specific race is JoinRaceForm below.
const RACE_SEED: SeedRace[] = [
  { id: "1", name: "Morning Sprint", host: "alex", wordCount: 50, players: 3, maxPlayers: 8, laneColorIndex: 0 },
  { id: "2", name: "Lunch Break Race", host: "sam", wordCount: 75, players: 6, maxPlayers: 10, laneColorIndex: 3 },
  { id: "3", name: "Speed Demons", host: "jordan", wordCount: 100, players: 2, maxPlayers: 6, laneColorIndex: 5 },
  { id: "4", name: "Casual Typing", host: "taylor", wordCount: 40, players: 4, maxPlayers: 4, laneColorIndex: 9 },
]

export function OpenRacesList() {
  const [races, setRaces] = useState(RACE_SEED)

  function handleJoin(id: string) {
    setRaces((prev) =>
      prev.map((race) =>
        race.id === id && race.players < race.maxPlayers
          ? { ...race, players: race.players + 1 }
          : race
      )
    )
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Open Races</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {races.map((race) => (
          <div
            key={race.id}
            className="flex items-center justify-between gap-3 rounded-md border bg-secondary/40 p-3"
          >
            <div className="flex items-center gap-3">
              <span
                className="h-3 w-3 shrink-0 rounded-full"
                style={{ backgroundColor: laneColor(race.laneColorIndex) }}
              />
              <div className="flex flex-col">
                <span className="text-sm font-medium">{race.name}</span>
                <span className="text-xs text-muted-foreground">
                  hosted by {race.host} · {race.wordCount} words · {race.players}/
                  {race.maxPlayers} players
                </span>
              </div>
            </div>
            <Button
              size="sm"
              variant="secondary"
              disabled={race.players >= race.maxPlayers}
              onClick={() => handleJoin(race.id)}
            >
              {race.players >= race.maxPlayers ? "Full" : "Join"}
            </Button>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}
