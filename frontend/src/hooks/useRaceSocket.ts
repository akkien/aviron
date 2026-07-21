import { useCallback, useEffect, useRef, useState } from "react"

import type { RaceFinishedMessage, RaceStateMessage } from "@/types/race"

// wsURL swaps VITE_API_URL's http(s) scheme for ws(s) — there's no separate
// WS base URL configured (frontend-realtime/websocket-client.md).
function wsURL(raceId: string, sessionToken: string): string {
  const base = new URL(import.meta.env.VITE_API_URL)
  const protocol = base.protocol === "https:" ? "wss:" : "ws:"
  const url = new URL(`${protocol}//${base.host}/ws`)
  url.searchParams.set("race_id", raceId)
  url.searchParams.set("session_token", sessionToken)
  return url.toString()
}

interface UseRaceSocketResult {
  raceState: RaceStateMessage | null
  finished: RaceFinishedMessage | null
  connectionError: string | null
  // leaving is true once the local player has clicked "Quit Race" —
  // frontend-realtime/reconnect-ui.md (next feature) will read this to
  // decide whether a subsequent close deserves a reconnect attempt.
  leaving: boolean
  sendTelemetry: (seq: number, wordsCorrect: number, paceWatt: number) => void
  leaveRace: () => void
}

// useRaceSocket owns one race's WebSocket connection lifecycle: opening on
// mount, sending join_race, tracking the latest race_state/race_finished
// messages, and exposing telemetry/leave_race senders. Extracted into its
// own hook (coding-standards.md's "extract reusable logic into custom
// hooks") rather than inlined in TypingView, since reconnect-ui.md needs to
// wrap this same connection logic with retry — a hook gives it a seam to
// extend instead of requiring a rewrite. No reconnect logic lives here yet;
// a dropped connection just leaves raceState/finished at their last values.
export function useRaceSocket(
  raceId: string | null,
  sessionToken: string | null,
): UseRaceSocketResult {
  const [raceState, setRaceState] = useState<RaceStateMessage | null>(null)
  const [finished, setFinished] = useState<RaceFinishedMessage | null>(null)
  const [connectionError, setConnectionError] = useState<string | null>(null)
  const [leaving, setLeaving] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!raceId || !sessionToken) return

    setRaceState(null)
    setFinished(null)
    setConnectionError(null)
    setLeaving(false)

    const ws = new WebSocket(wsURL(raceId, sessionToken))
    wsRef.current = ws

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: "join_race", race_id: raceId }))
    }

    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data) as { type: string }
      if (msg.type === "race_state") {
        setRaceState(msg as RaceStateMessage)
      } else if (msg.type === "race_finished") {
        setFinished(msg as RaceFinishedMessage)
        ws.close()
      }
    }

    ws.onerror = () => {
      setConnectionError("Connection error — the race may be unreachable.")
    }

    ws.onclose = () => {
      wsRef.current = null
    }

    return () => {
      ws.close()
      wsRef.current = null
    }
  }, [raceId, sessionToken])

  const sendTelemetry = useCallback((seq: number, wordsCorrect: number, paceWatt: number) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(
      JSON.stringify({
        type: "telemetry",
        seq,
        distance_m: wordsCorrect,
        pace_watt: paceWatt,
        ts: Date.now(),
      }),
    )
  }, [])

  const leaveRace = useCallback(() => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    setLeaving(true)
    ws.send(JSON.stringify({ type: "leave_race" }))
  }, [])

  return { raceState, finished, connectionError, leaving, sendTelemetry, leaveRace }
}
