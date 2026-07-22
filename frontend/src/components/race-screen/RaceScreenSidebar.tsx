import { useEffect, useRef, useState } from "react"

import { apiFetch } from "@/lib/api"
import { laneColor } from "@/lib/colors"
import { vehicleForUser, VEHICLES } from "@/lib/vehicles"
import type {
  Participant,
  RaceFinishedMessage,
  RaceStateMessage,
  RaceStatusResponse,
  StartRaceResponse,
} from "@/types/race"
import { Button } from "@/components/ui/button"
import { TypingBox } from "@/components/race-screen/TypingBox"

// formatCountdown renders whole seconds remaining as mm:ss, clamped to 0 —
// pending_expires_at is a fixed deadline computed once server-side
// (pending-expiry.md), so no server round-trip is needed per tick.
function formatCountdown(msRemaining: number): string {
  const totalSeconds = Math.max(0, Math.floor(msRemaining / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}:${seconds.toString().padStart(2, "0")}`
}

// Medal colors for the top 3 leaderboard ranks — separate from laneColor,
// which is the per-player identity color and stays untouched here; this is
// purely about the rank number itself.
function rankTextColor(rank: number): string {
  if (rank === 1) return "text-yellow-500"
  if (rank === 2) return "text-gray-400"
  if (rank === 3) return "text-amber-700"
  return "text-muted-foreground"
}

// Leaderboard needs no live raceState to render correctly — every distance
// falls back to 0, same as a race that hasn't moved yet — so it's shared
// as-is between a participant's active view and a spectator's read-only
// one (race-detail-cold-visit.md), not duplicated.
function Leaderboard({
  participants,
  raceState,
  distanceMeters,
}: {
  participants: Participant[]
  raceState: RaceStateMessage | null
  distanceMeters: number
}) {
  const ranked = [...participants].sort((a, b) => {
    const da = raceState?.participants.find((p) => p.user_id === a.user_id)?.distance_m ?? 0
    const db = raceState?.participants.find((p) => p.user_id === b.user_id)?.distance_m ?? 0
    return db - da
  })
  return (
    <div className="flex flex-col gap-2 rounded-2xl border-2 bg-card p-3.5">
      <div className="text-[11px] font-bold uppercase tracking-wider text-muted-foreground">
        Leaderboard
      </div>
      {ranked.map((p, i) => {
        const distanceM = raceState?.participants.find((rp) => rp.user_id === p.user_id)?.distance_m ?? 0
        const percent = distanceMeters > 0 ? Math.min(100, (distanceM / distanceMeters) * 100) : 0
        const color = laneColor(participants.findIndex((rp) => rp.user_id === p.user_id))
        return (
          <div key={p.user_id} className="flex items-center gap-2.5">
            <span className={`w-4 font-heading text-xs font-bold ${rankTextColor(i + 1)}`}>
              {i + 1}
            </span>
            <span className="h-3 w-3 shrink-0 rounded-full" style={{ backgroundColor: color }} />
            <span className="flex-1 truncate text-xs font-semibold">{p.display_name}</span>
            <span className="h-1.5 w-14 overflow-hidden rounded-full bg-secondary">
              <span
                className="block h-full rounded-full transition-[width] duration-400"
                style={{ width: `${percent}%`, backgroundColor: color }}
              />
            </span>
            <span className="w-8 text-right font-mono text-[11px] font-bold text-muted-foreground">
              {Math.round(percent)}%
            </span>
          </div>
        )
      })}
    </div>
  )
}

interface RaceScreenSidebarProps {
  raceId: string
  raceDetail: RaceStatusResponse | null
  currentUserId: string | null
  promptText: string | null
  onStarted: (promptText: string) => void
  onRefresh: () => void
  selectedVehicleId: string | null
  onSelectVehicle: (id: string) => void
  raceState: RaceStateMessage | null
  finished: RaceFinishedMessage | null
  connectionError: string | null
  leaving: boolean
  reconnecting: boolean
  evicted: boolean
  expired: boolean
  sendTelemetry: (seq: number, wordsCorrect: number, paceWatt: number) => void
  leaveRace: () => void
}

// Dispatches on race state, absorbing today's RaceStatusView (pending) and
// TypingView (active) logic rather than duplicating it — race-screen.md
// replaces both, not supplements them.
export function RaceScreenSidebar({
  raceId,
  raceDetail,
  currentUserId,
  promptText,
  onStarted,
  onRefresh,
  selectedVehicleId,
  onSelectVehicle,
  raceState,
  finished,
  connectionError,
  leaving,
  reconnecting,
  evicted,
  expired,
  sendTelemetry,
  leaveRace,
}: RaceScreenSidebarProps) {
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)
  const [copied, setCopied] = useState(false)
  const [now, setNow] = useState(() => Date.now())

  // Ticks once a second so the pending countdown re-renders — only runs
  // while there's actually a deadline to count down to.
  useEffect(() => {
    if (raceDetail?.status !== "pending" || !raceDetail.pending_expires_at) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [raceDetail?.status, raceDetail?.pending_expires_at])

  // The pending lobby's player list (below) renders from raceDetail.participants
  // — a one-time REST snapshot — not from the live raceState the socket
  // already receives on every join/leave (race_state carries no
  // display_name, so it can't replace raceDetail.participants outright).
  // Without this, a second player joining or leaving the lobby is only
  // ever reflected after a manual page refresh, even though the room actor
  // is already broadcasting the change. Re-fetching via onRefresh whenever
  // the live participant count disagrees with the REST snapshot closes
  // that gap — mirrors the same onRefresh call race_started already makes
  // (useRaceSocket.ts) to pull fresh data after a live event.
  const liveParticipantCount = raceState?.participants.length
  const lastRefreshedForCountRef = useRef<number | null>(null)
  useEffect(() => {
    if (!raceDetail || raceDetail.status !== "pending" || liveParticipantCount === undefined) return
    if (liveParticipantCount === raceDetail.participants.length) {
      lastRefreshedForCountRef.current = null
      return
    }
    if (lastRefreshedForCountRef.current === liveParticipantCount) return
    lastRefreshedForCountRef.current = liveParticipantCount
    onRefresh()
  }, [raceDetail, liveParticipantCount, onRefresh])

  if (raceDetail === null) {
    return <p className="text-sm text-muted-foreground">Loading...</p>
  }

  const isCreator = raceDetail.created_by === currentUserId
  const canStart = isCreator && raceDetail.status === "pending"
  const isParticipant = raceDetail.participants.some((p) => p.user_id === currentUserId)

  async function handleCopyRaceId() {
    await navigator.clipboard.writeText(raceId)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

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

  // Only "pending" gets the pre-race sidebar — "active"/"finished"/
  // "cancelled" all fall through to the state tree below, which already
  // branches correctly on leaving/evicted/finished/promptText. Guarding on
  // "=== pending" rather than "!== active" matters once a race finishes:
  // raceDetail.status can lag behind (nothing re-polls REST after the
  // race_finished WS message), so a stale "finished" status must not fall
  // back into showing the pre-race panel.
  if (raceDetail.status === "pending") {
    return (
      <div className="flex h-full flex-col gap-4">
        <div className="flex flex-col gap-2">
          <div className="text-xs font-bold uppercase tracking-wider text-primary">
            Race Room
          </div>
          <div className="font-heading text-2xl font-bold">{raceDetail.name}</div>
          {raceDetail.pending_expires_at && (
            <div className="text-xs text-muted-foreground">
              Closes in{" "}
              <span className="font-mono font-bold text-foreground">
                {formatCountdown(new Date(raceDetail.pending_expires_at).getTime() - now)}
              </span>{" "}
              if nobody starts it
            </div>
          )}
        </div>

        <div className="flex flex-col gap-2">
          <span className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
            Players ({raceDetail.participants.length})
          </span>
          {raceDetail.participants.map((p, i) => (
            <div
              key={p.user_id}
              className="flex items-center gap-2.5 rounded-xl border-2 bg-card px-2.5 py-2"
            >
              <span
                className="h-3.5 w-3.5 shrink-0 rounded-full"
                style={{ backgroundColor: laneColor(i) }}
              />
              <span className="flex-1 truncate text-sm font-medium">{p.display_name}</span>
              {p.user_id === currentUserId && (
                <span className="rounded-lg bg-primary/10 px-2 py-0.5 text-[10px] font-bold text-primary">
                  YOU
                </span>
              )}
              <span className="text-lg leading-none">
                {(p.user_id === currentUserId && selectedVehicleId
                  ? VEHICLES.find((v) => v.id === selectedVehicleId)
                  : vehicleForUser(p.user_id)
                )?.emoji}
              </span>
            </div>
          ))}
        </div>

        <div className="flex flex-col gap-2">
          <div className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
            Choose Your Vehicle
          </div>
          <div className="grid grid-cols-3 gap-2">
            {VEHICLES.map((v) => {
              const selected =
                selectedVehicleId === v.id ||
                (!selectedVehicleId && vehicleForUser(currentUserId ?? "").id === v.id)
              return (
                <button
                  key={v.id}
                  type="button"
                  onClick={() => onSelectVehicle(v.id)}
                  className={`flex flex-col items-center gap-1 rounded-xl border-2 px-1 py-2 ${
                    selected ? "border-primary bg-primary/10" : "border-border bg-card"
                  }`}
                >
                  <span className="text-xl leading-none">{v.emoji}</span>
                  <span className="text-[10px] font-bold text-muted-foreground">{v.label}</span>
                </button>
              )
            })}
          </div>
        </div>

        <div className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Race ID</span>
          <span className="truncate font-mono text-xs">{raceId}</span>
          <Button variant="outline" size="sm" onClick={handleCopyRaceId}>
            {copied ? "Copied!" : "Copy"}
          </Button>
        </div>

        {error && <p className="text-sm text-destructive">{error}</p>}

        <div className="mt-auto flex gap-2.5 pt-2">
          {canStart && (
            <Button className="flex-1" onClick={handleStart} disabled={starting}>
              {starting ? "Starting..." : "Start Race"}
            </Button>
          )}
          {isParticipant && (
            <Button variant="secondary" onClick={leaveRace}>
              Leave
            </Button>
          )}
        </div>
      </div>
    )
  }

  // Active race states below — restyled versions of today's TypingView
  // early-return states (reconnection/grace-period.md, leave-race.md); no
  // mockup exists for these (race-screen.md is explicit), just consistent
  // restyling.
  if (leaving) {
    return <p className="text-sm text-muted-foreground">You left the race.</p>
  }

  if (evicted) {
    return (
      <p className="text-sm text-muted-foreground">
        You were disconnected too long and have left the race.
      </p>
    )
  }

  if (expired) {
    return (
      <p className="text-sm text-muted-foreground">
        This race wasn't started in time and has been closed.
      </p>
    )
  }

  if (raceDetail.status === "cancelled") {
    return (
      <p className="text-sm text-muted-foreground">
        This race was cancelled — it wasn't started in time.
      </p>
    )
  }

  // Renders from the live race_finished message when present (freshest —
  // already correct for a client connected at the exact moment of
  // finishing), else from raceDetail.participants (REST, populated by the
  // same finish_rank/finish_time_ms columns for a cold visit — reload,
  // fresh link, a spectator — race-detail-cold-visit.md). One rendering
  // path, two data sources.
  if (finished || raceDetail.status === "finished") {
    const results = finished
      ? finished.results
      : raceDetail.participants.map((p) => ({
          user_id: p.user_id,
          finish_rank: p.finish_rank,
          finish_time_ms: p.finish_time_ms,
        }))
    return (
      <div className="flex flex-col gap-3">
        <div className="font-heading text-xl font-bold">Race finished</div>
        <ul className="flex flex-col gap-1.5 text-sm">
          {[...results]
            .sort((a, b) => (a.finish_rank ?? Infinity) - (b.finish_rank ?? Infinity))
            .map((r) => {
              const p = raceDetail.participants.find((p) => p.user_id === r.user_id)
              return (
                <li
                  key={r.user_id}
                  className="flex justify-between gap-4 rounded-lg border-2 bg-card px-3 py-2"
                >
                  <span className="font-medium">
                    <span className={rankTextColor(r.finish_rank ?? 0)}>#{r.finish_rank ?? "—"}</span>{" "}
                    {p?.display_name ?? r.user_id}
                  </span>
                  <span className="text-muted-foreground">
                    {r.finish_time_ms === null ? "DNF" : `${(r.finish_time_ms / 1000).toFixed(1)}s`}
                  </span>
                </li>
              )
            })}
        </ul>
      </div>
    )
  }

  // A visitor who never joined has no session token, so no live updates
  // are possible — a fully interactive TypingBox would silently do nothing
  // for them, so they get a read-only view instead and never need
  // promptText at all (race-detail-cold-visit.md).
  if (raceDetail.status === "active" && !isParticipant) {
    return (
      <div className="flex h-full min-h-0 flex-col gap-3">
        <div className="flex items-center justify-between">
          <div className="font-heading text-xl font-bold">{raceDetail.name}</div>
          <span className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
            Spectating
          </span>
        </div>
        <Leaderboard
          participants={raceDetail.participants}
          raceState={raceState}
          distanceMeters={raceDetail.distance_meters}
        />
      </div>
    )
  }

  if (promptText === null) {
    return (
      <p className="text-sm text-muted-foreground">{connectionError ?? "Loading prompt..."}</p>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <div className="flex items-center justify-between">
        <div className="font-heading text-xl font-bold">{raceDetail.name}</div>
        <Button variant="outline" size="sm" onClick={leaveRace}>
          Quit Race
        </Button>
      </div>

      {reconnecting && <p className="text-sm text-muted-foreground">Reconnecting...</p>}
      {connectionError && <p className="text-sm text-destructive">{connectionError}</p>}

      <Leaderboard
        participants={raceDetail.participants}
        raceState={raceState}
        distanceMeters={raceDetail.distance_meters}
      />

      <TypingBox
        promptText={promptText}
        distanceMeters={raceDetail.distance_meters}
        sendTelemetry={sendTelemetry}
      />
    </div>
  )
}
