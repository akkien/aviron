import { useState, type FormEvent } from "react"

import { apiFetch } from "@/lib/api"
import { floatStyle } from "@/lib/utils"
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
  onCreated: (raceId: string, sessionToken: string) => void
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
      onCreated(res.id, res.session_token)
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to create race")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card className="card-floating" style={floatStyle(6.5, -4)}>
      <CardHeader className="px-4 pb-0">
        <CardTitle className="text-base">Create a Race</CardTitle>
      </CardHeader>
      <form onSubmit={handleSubmit}>
        <CardContent className="flex flex-col gap-2 px-4 pt-2">
          <div className="grid grid-cols-3 gap-2">
            <div className="col-span-2 flex flex-col gap-1">
              <Label htmlFor="race-name" className="text-xs">
                Name
              </Label>
              <Input
                id="race-name"
                className="h-8 text-xs"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="race-distance" className="text-xs">
                Words
              </Label>
              <Input
                id="race-distance"
                type="number"
                min={1}
                className="h-8 font-mono text-xs"
                value={distanceMeters}
                onChange={(e) => setDistanceMeters(e.target.value)}
                required
              />
            </div>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
        </CardContent>
        <CardFooter className="px-4 pt-0">
          <Button type="submit" size="sm" className="w-full" disabled={!isValid || submitting}>
            {submitting ? "Creating..." : "Create race"}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}
