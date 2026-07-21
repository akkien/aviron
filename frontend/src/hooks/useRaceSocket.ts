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

// A few attempts a couple seconds apart (frontend-realtime/reconnect-ui.md)
// — deliberately not exponential backoff, this is a side-project test
// harness, not a production-grade reconnect strategy.
const RECONNECT_MAX_ATTEMPTS = 3
const RECONNECT_DELAY_MS = 2000

interface UseRaceSocketResult {
  raceState: RaceStateMessage | null
  finished: RaceFinishedMessage | null
  connectionError: string | null
  // leaving is true once the local player has clicked "Quit Race".
  leaving: boolean
  // reconnecting is true while a dropped connection is being retried — the
  // caller should keep showing the last-known raceState, not a loading
  // screen (reconnect-ui.md's "Reconnecting..." status).
  reconnecting: boolean
  // evicted is true once every reconnect attempt has failed — either the
  // grace period lapsed server-side, or the reattach was explicitly
  // refused. Named to match the backend's own vocabulary for this state
  // (RoomActor.evicted/IsEvicted/ParticipantEvicted), not a new metaphor.
  evicted: boolean
  sendTelemetry: (seq: number, wordsCorrect: number, paceWatt: number) => void
  leaveRace: () => void
}

// useRaceSocket owns one race's WebSocket connection lifecycle: opening,
// sending join_race, tracking the latest race_state/race_finished messages,
// exposing telemetry/leave_race senders, and — as of reconnect-ui.md —
// automatically retrying a connection that drops unexpectedly (not a
// self-initiated quit, not the race finishing) within a bounded number of
// attempts. Extracted into its own hook (coding-standards.md's "extract
// reusable logic into custom hooks") specifically so this retry logic could
// be added here, in one place, rather than requiring a wrapper or rewrite.
export function useRaceSocket(
  raceId: string | null,
  sessionToken: string | null,
): UseRaceSocketResult {
  const [raceState, setRaceState] = useState<RaceStateMessage | null>(null)
  const [finished, setFinished] = useState<RaceFinishedMessage | null>(null)
  const [connectionError, setConnectionError] = useState<string | null>(null)
  const [leaving, setLeaving] = useState(false)
  const [reconnecting, setReconnecting] = useState(false)
  const [evicted, setEvicted] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)
  // Mirrors of the `finished`/`leaving` state, readable synchronously from
  // inside onclose — a closure created earlier by the same effect run can't
  // see a state update from onmessage/leaveRace in the same tick, so a
  // plain state read here would be stale.
  const finishedRef = useRef(false)
  const leavingRef = useRef(false)
  const attemptRef = useRef(0)
  const reconnectTimerRef = useRef<number | null>(null)

  useEffect(() => {
    if (!raceId || !sessionToken) return
    const rid = raceId
    const token = sessionToken

    setRaceState(null)
    setFinished(null)
    setConnectionError(null)
    setLeaving(false)
    setReconnecting(false)
    setEvicted(false)
    finishedRef.current = false
    leavingRef.current = false
    attemptRef.current = 0

    // `stopped` is a plain closure variable scoped to *this* effect
    // execution, deliberately not a ref: React 18 StrictMode's dev-only
    // mount->cleanup->remount runs synchronously, so a second execution
    // would reset a shared ref before the first execution's now-stale
    // WebSocket's onclose actually fires (that fires later, as a queued
    // task) — a ref-based flag would read the wrong (reset) value and could
    // wrongly trigger a reconnect for a connection this exact effect run
    // already tore down. A closure variable can't leak across executions.
    let stopped = false

    function connect() {
      const ws = new WebSocket(wsURL(rid, token))
      wsRef.current = ws

      ws.onopen = () => {
        attemptRef.current = 0
        setReconnecting(false)
        setConnectionError(null)
        ws.send(JSON.stringify({ type: "join_race", race_id: rid }))
      }

      ws.onmessage = (event) => {
        const msg = JSON.parse(event.data) as { type: string }
        if (msg.type === "race_state") {
          setRaceState(msg as RaceStateMessage)
        } else if (msg.type === "race_finished") {
          finishedRef.current = true
          setFinished(msg as RaceFinishedMessage)
          ws.close()
        }
      }

      ws.onerror = () => {
        setConnectionError("Connection error — the race may be unreachable.")
      }

      ws.onclose = () => {
        // Only clear wsRef if it's still pointing at this exact connection
        // — a stale connection's onclose firing late (e.g. after React 18
        // StrictMode's dev-only double-invoke has already superseded it
        // with a newer one) must not null out a genuinely active socket
        // out from under sendTelemetry/leaveRace.
        if (wsRef.current === ws) {
          wsRef.current = null
        }

        // Intentional close — race finished, the player quit, or this
        // effect is tearing down — none of those deserve a reconnect.
        if (stopped || finishedRef.current || leavingRef.current) {
          return
        }

        if (attemptRef.current >= RECONNECT_MAX_ATTEMPTS) {
          // Every retry failed: either the grace period lapsed server-side,
          // or the reattach was explicitly refused (both surface through
          // this same onclose path — the browser doesn't distinguish a
          // rejected handshake from any other close).
          setReconnecting(false)
          setEvicted(true)
          return
        }

        attemptRef.current += 1
        setReconnecting(true)
        reconnectTimerRef.current = window.setTimeout(connect, RECONNECT_DELAY_MS)
      }
    }

    connect()

    return () => {
      stopped = true
      if (reconnectTimerRef.current !== null) {
        window.clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
      wsRef.current?.close()
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
    leavingRef.current = true
    setLeaving(true)
    ws.send(JSON.stringify({ type: "leave_race" }))
  }, [])

  return {
    raceState,
    finished,
    connectionError,
    leaving,
    reconnecting,
    evicted,
    sendTelemetry,
    leaveRace,
  }
}
