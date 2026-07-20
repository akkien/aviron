import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api"
import { laneColor } from "@/lib/colors"
import type { GetRaceTextResponse, Participant } from "@/types/race"
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card"

interface TypingViewProps {
  raceId: string
  distanceMeters: number
  participants: Participant[]
  currentUserId: string | null
  promptText: string | null
  onPromptTextFetched: (text: string) => void
}

// countCompletedWords approximates "words typed correctly" the same way a
// real WPM counter would: a word only counts once it's followed by
// whitespace, so a word still being typed doesn't count yet. The server
// never verifies typed text (see context/project-overview.md §13), so the
// client doesn't either — this is purely a progress estimate for the local
// player's own car.
function countCompletedWords(input: string): number {
  const tokens = input.split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return 0
  return input.endsWith(" ") ? tokens.length : tokens.length - 1
}

export function TypingView({
  raceId,
  distanceMeters,
  participants,
  currentUserId,
  promptText,
  onPromptTextFetched,
}: TypingViewProps) {
  const [input, setInput] = useState("")
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (promptText !== null) return
    apiFetch<GetRaceTextResponse>(`/races/${raceId}/text`)
      .then((res) => onPromptTextFetched(res.prompt_text))
      .catch((err) => setError(err instanceof Error ? err.message : "failed to load prompt text"))
  }, [raceId, promptText, onPromptTextFetched])

  if (promptText === null) {
    return (
      <Card>
        <CardContent className="pt-6">
          <p className="text-sm text-muted-foreground">
            {error ?? "Loading prompt..."}
          </p>
        </CardContent>
      </Card>
    )
  }

  const typed = promptText.slice(0, input.length)
  const remaining = promptText.slice(input.length)
  const wordsCompleted = Math.min(countCompletedWords(input), distanceMeters)
  const progressPercent = (wordsCompleted / distanceMeters) * 100

  return (
    <Card>
      <CardHeader>
        <CardTitle>Type the prompt</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="rounded-md border bg-muted/50 p-3 font-mono text-sm leading-relaxed">
          <span className="text-muted-foreground">{typed}</span>
          <span>{remaining}</span>
        </p>
        <textarea
          className="min-h-20 w-full rounded-md border border-input bg-transparent p-3 font-mono text-sm shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Start typing..."
        />
        <div className="flex flex-col gap-3">
          {participants.map((p, i) => (
            <div key={p.user_id} className="flex items-center gap-2">
              <span className="w-32 shrink-0 truncate text-sm">{p.display_name}</span>
              <div className="relative h-3 w-full rounded-full bg-secondary">
                <div
                  className="absolute top-1/2 h-4 w-4 rounded-full border-2 border-background shadow transition-all"
                  style={{
                    left: `${p.user_id === currentUserId ? progressPercent : 0}%`,
                    backgroundColor: laneColor(i),
                    transform: "translate(-50%, -50%)",
                  }}
                />
              </div>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
