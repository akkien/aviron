import { useEffect, useRef, useState } from "react"

import { apiFetch } from "@/lib/api"
import { laneColor } from "@/lib/colors"
import { useRaceSocket } from "@/hooks/useRaceSocket"
import type { GetRaceTextResponse, Participant } from "@/types/race"
import { Button } from "@/components/ui/button"
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card"

interface TypingViewProps {
  raceId: string
  sessionToken: string | null
  distanceMeters: number
  participants: Participant[]
  promptText: string | null
  onPromptTextFetched: (text: string) => void
}

// countCompletedWords approximates "words typed correctly" the same way a
// real WPM counter would: a word only counts once it's followed by
// whitespace, so a word still being typed doesn't count yet. The server
// never verifies typed text (see context/project-overview.md §13), so the
// client doesn't either — this drives both the local textarea diff and what
// gets sent as telemetry.
function countCompletedWords(input: string): number {
  const tokens = input.split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return 0
  return input.endsWith(" ") ? tokens.length : tokens.length - 1
}

export function TypingView({
  raceId,
  sessionToken,
  distanceMeters,
  participants,
  promptText,
  onPromptTextFetched,
}: TypingViewProps) {
  const [input, setInput] = useState("")
  const [error, setError] = useState<string | null>(null)

  const { raceState, finished, connectionError, leaving, sendTelemetry, leaveRace } =
    useRaceSocket(raceId, sessionToken)

  const seqRef = useRef(0)
  const lastSentWordsRef = useRef(0)
  const startedAtRef = useRef<number | null>(null)

  useEffect(() => {
    if (promptText !== null) return
    apiFetch<GetRaceTextResponse>(`/races/${raceId}/text`)
      .then((res) => onPromptTextFetched(res.prompt_text))
      .catch((err) => setError(err instanceof Error ? err.message : "failed to load prompt text"))
  }, [raceId, promptText, onPromptTextFetched])

  // Send one telemetry message per newly-completed word (context/project
  // -overview.md §13's per-word cadence, not a fixed timer) — stops once
  // the race has finished or the local player has quit, since there's
  // nothing left to report.
  useEffect(() => {
    if (finished || leaving) return
    const wordsCompleted = Math.min(countCompletedWords(input), distanceMeters)
    if (wordsCompleted <= lastSentWordsRef.current) return

    if (startedAtRef.current === null) startedAtRef.current = Date.now()
    const elapsedMinutes = (Date.now() - startedAtRef.current) / 60000
    const paceWatt = elapsedMinutes > 0 ? Math.round(wordsCompleted / elapsedMinutes) : 0

    lastSentWordsRef.current = wordsCompleted
    seqRef.current += 1
    sendTelemetry(seqRef.current, wordsCompleted, paceWatt)
  }, [input, distanceMeters, finished, leaving, sendTelemetry])

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

  if (leaving) {
    return (
      <Card>
        <CardContent className="pt-6">
          <p className="text-sm text-muted-foreground">You left the race.</p>
        </CardContent>
      </Card>
    )
  }

  if (finished) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Race finished</CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="flex flex-col gap-1 text-sm">
            {[...finished.results]
              .sort((a, b) => (a.finish_rank ?? Infinity) - (b.finish_rank ?? Infinity))
              .map((r) => {
                const p = participants.find((p) => p.user_id === r.user_id)
                return (
                  <li key={r.user_id} className="flex justify-between gap-4">
                    <span>
                      #{r.finish_rank ?? "—"} {p?.display_name ?? r.user_id}
                    </span>
                    <span className="text-muted-foreground">
                      {r.finish_time_ms === null ? "DNF" : `${(r.finish_time_ms / 1000).toFixed(1)}s`}
                    </span>
                  </li>
                )
              })}
          </ul>
        </CardContent>
      </Card>
    )
  }

  const typed = promptText.slice(0, input.length)
  const remaining = promptText.slice(input.length)

  const progressByUserId = new Map(raceState?.participants.map((p) => [p.user_id, p]) ?? [])

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>Type the prompt</CardTitle>
        <Button variant="outline" size="sm" onClick={leaveRace}>
          Quit Race
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {connectionError && <p className="text-sm text-destructive">{connectionError}</p>}
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
          {participants.map((p, i) => {
            // Every lane, including the local player's own, is driven by
            // the latest race_state tick from the server (keyed by
            // user_id) — no special-casing the current user against local
            // input, unlike Phase 1's version of this view.
            const distanceM = progressByUserId.get(p.user_id)?.distance_m ?? 0
            const percent = (distanceM / distanceMeters) * 100
            return (
              <div key={p.user_id} className="flex items-center gap-2">
                <span className="w-32 shrink-0 truncate text-sm">{p.display_name}</span>
                <div className="relative h-3 w-full rounded-full bg-secondary">
                  <div
                    className="absolute top-1/2 h-4 w-4 rounded-full border-2 border-background shadow transition-all"
                    style={{
                      left: `${percent}%`,
                      backgroundColor: laneColor(i),
                      transform: "translate(-50%, -50%)",
                    }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}
