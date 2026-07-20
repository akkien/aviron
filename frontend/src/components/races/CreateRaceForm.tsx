import { useState, type FormEvent } from "react"

import { apiFetch } from "@/lib/api"
import type { CreateRaceResponse } from "@/types/race"
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

interface CreateRaceFormProps {
  onCreated: (raceId: string) => void
}

export function CreateRaceForm({ onCreated }: CreateRaceFormProps) {
  const [name, setName] = useState("")
  const [distanceMeters, setDistanceMeters] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const wordCount = Number(distanceMeters)
  const isValid = name.trim() !== "" && Number.isInteger(wordCount) && wordCount > 0

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!isValid) return

    setError(null)
    setSubmitting(true)
    try {
      const res = await apiFetch<CreateRaceResponse>("/races", {
        method: "POST",
        body: JSON.stringify({ name, distance_meters: wordCount }),
      })
      onCreated(res.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to create race")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Create race</CardTitle>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="race-name">Name</Label>
            <Input
              id="race-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="race-distance">Target word count</Label>
            <Input
              id="race-distance"
              type="number"
              min={1}
              value={distanceMeters}
              onChange={(e) => setDistanceMeters(e.target.value)}
              required
            />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
        <CardFooter>
          <Button type="submit" disabled={!isValid || submitting}>
            {submitting ? "Creating..." : "Create race"}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
