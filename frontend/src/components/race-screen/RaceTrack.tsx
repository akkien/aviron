import { laneColor } from "@/lib/colors"
import { vehicleForUser, VEHICLES } from "@/lib/vehicles"
import type { Participant, RaceStateMessage } from "@/types/race"

interface RaceTrackProps {
  status: string
  participants: Participant[]
  raceState: RaceStateMessage | null
  distanceMeters: number
  currentUserId: string | null
  localVehicleId: string | null
}

function statusLabel(status: string, playerCount: number): string {
  if (status === "pending") return `${playerCount} Racers Ready`
  if (status === "active") return "Race In Progress"
  if (status === "finished") return "Race Finished"
  return status
}

// RaceTrack is present whenever a race exists, any status — pre-race it
// shows every participant lined up at 0%, driven purely by raceState once
// active. Lane color/order must match the sidebar list's laneColor(index)
// exactly (race-screen.md) so the two panels never disagree about which
// color belongs to which player.
export function RaceTrack({
  status,
  participants,
  raceState,
  distanceMeters,
  currentUserId,
  localVehicleId,
}: RaceTrackProps) {
  const progressByUserId = new Map(raceState?.participants.map((p) => [p.user_id, p]) ?? [])

  function vehicleFor(userId: string) {
    if (userId === currentUserId && localVehicleId) {
      return VEHICLES.find((v) => v.id === localVehicleId) ?? vehicleForUser(userId)
    }
    return vehicleForUser(userId)
  }

  return (
    <div className="relative flex h-full w-full flex-col overflow-hidden rounded-2xl bg-gradient-to-b from-sky-200 via-amber-100 to-lime-100 p-6">
      <div className="mb-4 flex min-h-[34px] items-center justify-center gap-4">
        <span className="shrink-0 whitespace-nowrap rounded-full bg-card/80 px-3.5 py-1.5 font-mono text-sm font-bold text-primary">
          {statusLabel(status, participants.length)}
        </span>
      </div>
      <div className="relative flex flex-1 flex-col justify-around gap-2 rounded-xl bg-card/50 py-5">
        <div className="absolute bottom-2 left-16 top-6 z-[2] w-0 border-l-4 border-dashed border-foreground/20" />
        <span className="absolute left-16 top-0 z-[3] rounded bg-card px-1.5 py-0.5 text-[10px] font-bold tracking-wide text-muted-foreground">
          START
        </span>
        <div
          className="absolute bottom-2 right-9 top-2 z-[2] w-3.5 rounded"
          style={{
            backgroundImage:
              "repeating-conic-gradient(oklch(0.2 0 0) 0% 25%, oklch(0.98 0 0) 0% 50%)",
            backgroundSize: "12px 12px",
          }}
        />
        <span className="absolute right-5 -top-2.5 z-[3] text-base">🏁</span>

        {participants.map((p, i) => {
          const distanceM = progressByUserId.get(p.user_id)?.distance_m ?? 0
          const percent = distanceMeters > 0 ? Math.min(100, (distanceM / distanceMeters) * 100) : 0
          const vehicle = vehicleFor(p.user_id)
          const color = laneColor(i)
          return (
            <div key={p.user_id} className="relative mx-5 min-h-20 flex-1 overflow-hidden rounded-xl">
              {/* Lane band sized to actually contain the vehicle icon
                  (text-3xl, ~36px) — an 8px sliver made the vehicle look
                  like it was floating outside its own lane, only its
                  center touching the colored strip. */}
              <div
                className="absolute top-1/2 left-0 h-6 -translate-y-1/2 rounded-lg transition-[width] duration-500 ease-out"
                style={{ width: `${percent}%`, backgroundColor: color, opacity: 0.35 }}
              />
              {/* Only the vehicle span is the thing being vertically
                  centered — it must align with the lane band's own center
                  above. The name label used to sit above it in the same
                  flex column, so the *stack's* midpoint (not the vehicle's)
                  landed on the lane center, visibly pushing the vehicle
                  below the band. The label is now positioned absolutely off
                  the vehicle instead, so it no longer affects what's being
                  centered.

                  `left: {percent}%` alone positions this wrapper's own LEFT
                  edge at that offset — at 100% that puts the entire vehicle
                  body past the lane's right edge, invisible behind
                  overflow-hidden the instant it "finishes." translateX
                  blends from 0 (left edge anchored, same as before) to
                  -100% of the wrapper's own width (right edge anchored) as
                  percent grows, so the vehicle eases into staying fully
                  inside the lane by the time it reaches the finish line,
                  instead of snapping out of view. */}
              <div
                className="absolute top-1/2 z-[5] transition-all duration-500 ease-out"
                style={{ left: `${percent}%`, transform: `translate(-${percent}%, -50%)` }}
              >
                <span
                  className="absolute -top-5 left-1/2 -translate-x-1/2 whitespace-nowrap rounded-full px-2 py-0.5 text-[10px] font-bold text-white shadow"
                  style={{ backgroundColor: color }}
                >
                  {p.display_name}
                </span>
                <span className="block text-3xl leading-none drop-shadow scale-x-[-1]">
                  {vehicle.emoji}
                </span>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
