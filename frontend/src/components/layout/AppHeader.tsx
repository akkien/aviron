import { useNavigate } from "react-router-dom"

import { getEmail, logout } from "@/lib/auth"
import { Button } from "@/components/ui/button"

function initialsFromEmail(email: string | null): string {
  if (!email) return "?"
  const localPart = email.split("@")[0]
  return localPart.slice(0, 2).toUpperCase()
}

export function AppHeader() {
  const navigate = useNavigate()
  const email = getEmail()

  function handleLogout() {
    logout()
    navigate("/login")
  }

  return (
    // No longer position:fixed — RacesPage's root is now h-screen with
    // overflow-hidden, so the whole page never scrolls in the first place
    // and a normal-flow header stays in view without needing to escape the
    // document flow. Constrained to the same max-w-325 as the page content
    // below (not the design's own edge-to-edge treatment), so the navbar
    // lines up with the rest of the page instead of spanning the viewport.
    <header className="flex shrink-0 justify-center border-b bg-card px-10 py-3">
      <div className="flex w-full max-w-325 items-center justify-between">
        <span className="heading-pop font-heading text-2xl font-extrabold text-primary">
          Aviron
        </span>
        <div className="flex items-center gap-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">
            {initialsFromEmail(email)}
          </div>
          <span className="text-sm font-medium text-muted-foreground">{email}</span>
          <Button variant="outline" size="sm" className="rounded-full" onClick={handleLogout}>
            Log Out
          </Button>
        </div>
      </div>
    </header>
  )
}
