import { useState } from "react"

import { apiFetch } from "@/lib/api"
import type { RaceStatusResponse, StartRaceResponse } from "@/types/race"
import { Button } from "@/components/ui/button"
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card"

interface RaceStatusViewProps {
  raceId: string
  raceDetail: RaceStatusResponse | null
  currentUserId: string | null
  onRefresh: () => void
  onStarted: (promptText: string) => void
}

export function RaceStatusView({
  raceId,
  raceDetail,
  currentUserId,
  onRefresh,
  onStarted,
}: RaceStatusViewProps) {
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  const isCreator = raceDetail !== null && raceDetail.created_by === currentUserId
  const canStart = isCreator && raceDetail?.status === "pending"

  async function handleStart() {
    setError(null)
    setStarting(true)
    try {
      const res = await apiFetch<StartRaceResponse>(`/races/${raceId}/start`, {
        method: "POST",
      })
      onStarted(res.prompt_text)
      onRefresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to start race")
    } finally {
      setStarting(false)
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>Race status</CardTitle>
        <Button variant="outline" size="sm" onClick={onRefresh}>
          Refresh
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {raceDetail === null ? (
          <p className="text-sm text-muted-foreground">Loading...</p>
        ) : (
          <>
            <dl className="grid grid-cols-2 gap-1 text-sm">
              <dt className="text-muted-foreground">Name</dt>
              <dd>{raceDetail.name}</dd>
              <dt className="text-muted-foreground">Status</dt>
              <dd>{raceDetail.status}</dd>
              <dt className="text-muted-foreground">Target word count</dt>
              <dd>{raceDetail.distance_meters}</dd>
            </dl>
            <div>
              <p className="mb-1 text-sm font-medium">Participants</p>
              <ul className="flex flex-col gap-1 text-sm text-muted-foreground">
                {raceDetail.participants.map((p) => (
                  <li key={p.user_id}>
                    {p.display_name} — joined {new Date(p.joined_at).toLocaleTimeString()}
                  </li>
                ))}
              </ul>
            </div>
            {canStart && (
              <Button onClick={handleStart} disabled={starting}>
                {starting ? "Starting..." : "Start race"}
              </Button>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
          </>
        )}
      </CardContent>
    </Card>
  )
}
