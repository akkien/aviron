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
    <header className="flex items-center justify-between rounded-lg border bg-card px-4 py-2.5 shadow-xs">
      <span className="font-heading text-lg font-bold text-primary">Aviron</span>
      <div className="flex items-center gap-2">
        <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">
          {initialsFromEmail(email)}
        </div>
        <span className="text-sm text-muted-foreground">{email}</span>
        <Button variant="outline" size="sm" onClick={handleLogout}>
          Log Out
        </Button>
      </div>
    </header>
  )
}
