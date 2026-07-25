import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api"
import { floatStyle } from "@/lib/utils"
import type { LeaderboardTopResponse } from "@/types/leaderboard"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

type LeaderboardWindow = "alltime" | "weekly"

export function RankedLeaderboard() {
  const [activeWindow, setActiveWindow] = useState<LeaderboardWindow>("alltime")
  const [page, setPage] = useState(1)
  const [data, setData] = useState<LeaderboardTopResponse | null>(null)

  // Pagination is server-side (5 entries per page, fixed by the backend —
  // see LeaderboardService.pageSize) — this fetches exactly one page at a
  // time rather than over-fetching and slicing client-side.
  //
  // `cancelled` guards against a real race: clicking next/prev quickly
  // fires one request per page change, and responses aren't guaranteed to
  // arrive in the order they were sent — an earlier page's request can
  // resolve *after* a later one. Without this guard, that stale response
  // would overwrite the newer data, flashing the wrong page's entries
  // under the current page number (mirrors OpenRacesList's same pattern).
  useEffect(() => {
    let cancelled = false
    setData(null)
    apiFetch<LeaderboardTopResponse>(`/leaderboard?window=${activeWindow}&page=${page}`)
      .then((res) => {
        if (!cancelled) setData(res)
      })
      .catch(() => {
        if (!cancelled) setData(null)
      })
    return () => {
      cancelled = true
    }
  }, [activeWindow, page])

  function switchWindow(w: LeaderboardWindow) {
    setActiveWindow(w)
    setPage(1)
  }

  const totalPages = data?.total_pages ?? 1

  return (
    <Card className="h-full card-floating" style={floatStyle(7, -2)}>
      <CardHeader className="flex-row items-center justify-between px-4 pb-0">
        <CardTitle className="text-base">Leaderboard</CardTitle>
        <div className="flex items-center gap-1.5">
          <Button
            size="sm"
            className="rounded-full"
            variant={activeWindow === "alltime" ? "default" : "secondary"}
            onClick={() => switchWindow("alltime")}
          >
            All-Time
          </Button>
          <Button
            size="sm"
            className="rounded-full"
            variant={activeWindow === "weekly" ? "default" : "secondary"}
            onClick={() => switchWindow("weekly")}
          >
            This Week
          </Button>
          <Button
            size="icon"
            variant="ghost"
            className="h-7 w-7"
            disabled={page <= 1}
            onClick={() => setPage((p) => Math.max(1, p - 1))}
          >
            ‹
          </Button>
          <span className="font-mono text-xs text-muted-foreground">
            {page} / {totalPages}
          </span>
          <Button
            size="icon"
            variant="ghost"
            className="h-7 w-7"
            disabled={page >= totalPages}
            onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
          >
            ›
          </Button>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 px-4 pt-2">
        {data === null && <p className="text-sm text-muted-foreground">Loading...</p>}
        {data !== null && data.entries.length === 0 && (
          <p className="text-sm text-muted-foreground">No results yet</p>
        )}
        {data?.entries.map((entry) => (
          <div
            key={entry.user_id}
            className="flex items-center gap-3 rounded-md border bg-secondary/40 px-2.5 py-1"
          >
            <span className="w-5 shrink-0 font-heading text-xs font-bold text-muted-foreground">
              #{entry.rank}
            </span>
            <div className="flex min-w-0 flex-1 items-baseline gap-2">
              <span className="truncate text-xs font-semibold">{entry.display_name}</span>
              <span className="shrink-0 text-[10px] text-muted-foreground">
                {entry.wins} wins · {entry.races} races
              </span>
            </div>
            <span className="shrink-0 font-heading text-xs font-bold text-primary">
              {entry.avg_wpm.toFixed(2)} WPM
            </span>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}
