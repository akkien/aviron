import { useState } from "react"

import { apiFetch } from "@/lib/api"
import { laneColor } from "@/lib/colors"
import { vehicleForUser, VEHICLES } from "@/lib/vehicles"
import type {
  LeaveRaceResponse,
  RaceFinishedMessage,
  RaceStateMessage,
  RaceStatusResponse,
  StartRaceResponse,
} from "@/types/race"
import { Button } from "@/components/ui/button"
import { TypingBox } from "@/components/race-screen/TypingBox"

interface RaceScreenSidebarProps {
  raceId: string
  raceDetail: RaceStatusResponse | null
  currentUserId: string | null
  promptText: string | null
  onStarted: (promptText: string) => void
  onRefresh: () => void
  onLeftRace: () => void
  selectedVehicleId: string | null
  onSelectVehicle: (id: string) => void
  raceState: RaceStateMessage | null
  finished: RaceFinishedMessage | null
  connectionError: string | null
  leaving: boolean
  reconnecting: boolean
  evicted: boolean
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
  onLeftRace,
  selectedVehicleId,
  onSelectVehicle,
  raceState,
  finished,
  connectionError,
  leaving,
  reconnecting,
  evicted,
  sendTelemetry,
  leaveRace,
}: RaceScreenSidebarProps) {
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)
  const [leavingPending, setLeavingPending] = useState(false)
  const [copied, setCopied] = useState(false)

  if (raceDetail === null) {
    return <p className="text-sm text-muted-foreground">Loading...</p>
  }

  const isCreator = raceDetail.created_by === currentUserId
  const canStart = isCreator && raceDetail.status === "pending"

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

  async function handleLeavePending() {
    setError(null)
    setLeavingPending(true)
    try {
      await apiFetch<LeaveRaceResponse>(`/races/${raceId}/leave`, { method: "POST" })
      onLeftRace()
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to leave race")
      setLeavingPending(false)
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
        </div>

        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <span className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
              Players ({raceDetail.participants.length})
            </span>
            <Button variant="ghost" size="sm" onClick={onRefresh}>
              Refresh
            </Button>
          </div>
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
          <Button variant="secondary" onClick={handleLeavePending} disabled={leavingPending}>
            {leavingPending ? "Leaving..." : "Leave"}
          </Button>
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

  if (finished) {
    return (
      <div className="flex flex-col gap-3">
        <div className="font-heading text-xl font-bold">Race finished</div>
        <ul className="flex flex-col gap-1.5 text-sm">
          {[...finished.results]
            .sort((a, b) => (a.finish_rank ?? Infinity) - (b.finish_rank ?? Infinity))
            .map((r) => {
              const p = raceDetail.participants.find((p) => p.user_id === r.user_id)
              return (
                <li
                  key={r.user_id}
                  className="flex justify-between gap-4 rounded-lg border-2 bg-card px-3 py-2"
                >
                  <span className="font-medium">
                    #{r.finish_rank ?? "—"} {p?.display_name ?? r.user_id}
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

  if (promptText === null) {
    return (
      <p className="text-sm text-muted-foreground">{connectionError ?? "Loading prompt..."}</p>
    )
  }

  const rankedParticipants = [...raceDetail.participants].sort((a, b) => {
    const da = raceState?.participants.find((p) => p.user_id === a.user_id)?.distance_m ?? 0
    const db = raceState?.participants.find((p) => p.user_id === b.user_id)?.distance_m ?? 0
    return db - da
  })

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

      <div className="flex flex-col gap-2 rounded-2xl border-2 bg-card p-3.5">
        <div className="text-[11px] font-bold uppercase tracking-wider text-muted-foreground">
          Leaderboard
        </div>
        {rankedParticipants.map((p, i) => {
          const distanceM = raceState?.participants.find((rp) => rp.user_id === p.user_id)?.distance_m ?? 0
          const percent =
            raceDetail.distance_meters > 0
              ? Math.min(100, (distanceM / raceDetail.distance_meters) * 100)
              : 0
          const color = laneColor(raceDetail.participants.findIndex((rp) => rp.user_id === p.user_id))
          return (
            <div key={p.user_id} className="flex items-center gap-2.5">
              <span className="w-4 font-heading text-xs font-bold text-muted-foreground">
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

      <TypingBox
        promptText={promptText}
        distanceMeters={raceDetail.distance_meters}
        sendTelemetry={sendTelemetry}
      />
    </div>
  )
}
