import { useState, type FormEvent } from "react"

import { apiFetch } from "@/lib/api"
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
    <Card className="border-2">
      <CardHeader className="pb-2">
        <CardTitle>Have a race ID?</CardTitle>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-end gap-2">
            <div className="flex flex-1 flex-col gap-1.5">
              <Label htmlFor="join-race-id">Race ID</Label>
              <Input
                id="join-race-id"
                value={raceId}
                onChange={(e) => setRaceId(e.target.value)}
                required
              />
            </div>
            <Button type="submit" disabled={!raceId.trim() || submitting}>
              {submitting ? "Joining..." : "Join race"}
            </Button>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
      </form>
    </Card>
  )
}
