import { useState, type FormEvent } from "react"

import { apiFetch } from "@/lib/api"
import { floatStyle } from "@/lib/utils"
import type { JoinRaceResponse } from "@/types/race"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card"

interface JoinRaceFormProps {
  onJoined: (raceId: string, sessionToken: string) => void
}

export function JoinRaceForm({ onJoined }: JoinRaceFormProps) {
  const [raceId, setRaceId] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!raceId.trim()) return

    setError(null)
    setSubmitting(true)
    try {
      const res = await apiFetch<JoinRaceResponse>(`/races/${raceId}/join`, {
        method: "POST",
      })
      onJoined(raceId, res.session_token)
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to join race")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card className="card-floating" style={floatStyle(5.5, -1)}>
      <CardHeader className="px-4 pb-0">
        <CardTitle className="text-base">Have a race ID?</CardTitle>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="flex flex-col gap-1 px-4 pt-2">
          <Label htmlFor="join-race-id" className="text-xs">
            Race ID
          </Label>
          <div className="flex gap-1.5">
            <Input
              id="join-race-id"
              className="h-8 flex-1 font-mono text-xs"
              value={raceId}
              onChange={(e) => setRaceId(e.target.value)}
              required
            />
            <Button type="submit" size="sm" disabled={!raceId.trim() || submitting}>
              {submitting ? "Joining..." : "Join race"}
            </Button>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </CardContent>
      </form>
    </Card>
  )
}
