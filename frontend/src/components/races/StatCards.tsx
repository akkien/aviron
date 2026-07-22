import { useEffect, useState } from "react"

import { Card } from "@/components/ui/card"
import { apiFetch } from "@/lib/api"
import type { LeaderboardMeResponse } from "@/types/leaderboard"

// Only 3 cards ship — the mockup's 4th card (Avg Accuracy) is dropped
// outright, not built here, since user-stats.md already decided it will
// never be real (unmeasurable under this project's never-verify-typed-text
// trust model).
function buildStats(data: LeaderboardMeResponse) {
  return [
    { label: "Races Joined", value: String(data.races_joined), valueClassName: "text-primary" },
    { label: "Races Won", value: String(data.races_won), valueClassName: "text-destructive" },
    { label: "Avg WPM", value: data.avg_wpm.toFixed(2), valueClassName: "text-green-600" },
  ]
}

export function StatCards() {
  const [data, setData] = useState<LeaderboardMeResponse | null>(null)

  useEffect(() => {
    apiFetch<LeaderboardMeResponse>("/leaderboard/me")
      .then(setData)
      .catch(() => setData({ races_joined: 0, races_won: 0, avg_wpm: 0 }))
  }, [])

  if (data === null) {
    return <p className="text-sm text-muted-foreground">Loading...</p>
  }

  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {buildStats(data).map((stat) => (
        <Card key={stat.label} className="flex flex-row items-center justify-between px-4 py-3">
          <span className="text-sm text-muted-foreground">{stat.label}</span>
          <span className={`font-heading text-2xl font-bold ${stat.valueClassName}`}>
            {stat.value}
          </span>
        </Card>
      ))}
    </div>
  )
}
