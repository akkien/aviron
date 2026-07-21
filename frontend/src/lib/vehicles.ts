export interface Vehicle {
  id: string
  label: string
  emoji: string
}

// 10 vehicles — one per MaxParticipants (internal/race.MaxParticipants) so
// a full room can plausibly show 10 distinct icons, though vehicleForUser's
// hash doesn't guarantee uniqueness (purely cosmetic, no server-side slot
// reservation).
export const VEHICLES: readonly Vehicle[] = [
  { id: "car", label: "Car", emoji: "🏎️" },
  { id: "bike", label: "Bike", emoji: "🏍️" },
  { id: "rocket", label: "Rocket", emoji: "🚀" },
  { id: "van", label: "Van", emoji: "🚐" },
  { id: "truck", label: "Truck", emoji: "🚚" },
  { id: "boat", label: "Boat", emoji: "🚤" },
  { id: "plane", label: "Plane", emoji: "✈️" },
  { id: "train", label: "Train", emoji: "🚂" },
  { id: "scooter", label: "Scooter", emoji: "🛵" },
  { id: "helicopter", label: "Helicopter", emoji: "🚁" },
]

// vehicleForUser deterministically hashes userId into VEHICLES so every
// viewer renders the same opponent icon without any server sync — vehicles
// are purely cosmetic (phase-2.5-plan.md's scope decision), the room actor
// never broadcasts a vehicle field.
export function vehicleForUser(userId: string): Vehicle {
  let hash = 0
  for (let i = 0; i < userId.length; i++) {
    hash = (hash * 31 + userId.charCodeAt(i)) | 0
  }
  return VEHICLES[Math.abs(hash) % VEHICLES.length]
}
