import { useEffect, useState } from "react"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { apiFetch } from "@/lib/api"
import type { LeaderboardMeResponse } from "@/types/leaderboard"

// Only 3 rows ship — the mockup's 4th stat (Avg Accuracy) is dropped
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

  return (
    <Card className="h-full">
      <CardHeader className="px-4 pb-0">
        <CardTitle className="text-base">Your Stats</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1.5 px-4 pt-2">
        {data === null && <p className="text-xs text-muted-foreground">Loading...</p>}
        {data !== null &&
          buildStats(data).map((stat) => (
            <div key={stat.label} className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground">{stat.label}</span>
              <span className={`font-heading text-xl font-bold ${stat.valueClassName}`}>
                {stat.value}
              </span>
            </div>
          ))}
      </CardContent>
    </Card>
  )
}
