import { Card } from "@/components/ui/card"

// Hardcoded placeholder values pending user-stats/user-stats.md, which
// replaces this with a real GET /leaderboard/me call. Only 3 cards ship —
// the mockup's 4th card (Avg Accuracy) is dropped outright, not built here,
// since user-stats.md already decided it will never be real (unmeasurable
// under this project's never-verify-typed-text trust model).
const STATS = [
  { label: "Races Joined", value: "12", valueClassName: "text-primary" },
  { label: "Races Won", value: "4", valueClassName: "text-destructive" },
  { label: "Avg WPM", value: "48", valueClassName: "text-green-600" },
]

export function StatCards() {
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {STATS.map((stat) => (
        <Card key={stat.label} className="flex flex-row items-center justify-between px-4 py-3">
          <span className="text-sm text-muted-foreground">{stat.label}</span>
          <span className={`font-heading text-2xl font-bold ${stat.valueClassName}`}>
            {stat.value}
          </span>
        </Card>
      ))}
    </div>
  )
}
