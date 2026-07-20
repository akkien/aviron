import { useState, type FormEvent } from "react"

import { apiFetch } from "@/lib/api"
import type { JoinRaceResponse } from "@/types/race"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
  CardFooter,
} from "@/components/ui/card"

interface JoinRaceFormProps {
  onJoined: (raceId: string) => void
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
      await apiFetch<JoinRaceResponse>(`/races/${raceId}/join`, {
        method: "POST",
      })
      onJoined(raceId)
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to join race")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Join race</CardTitle>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="join-race-id">Race ID</Label>
            <Input
              id="join-race-id"
              value={raceId}
              onChange={(e) => setRaceId(e.target.value)}
              required
            />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={!raceId.trim() || submitting}>
            {submitting ? "Joining..." : "Join race"}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
