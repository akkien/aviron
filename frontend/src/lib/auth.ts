const TOKEN_KEY = "aviron_token"

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

export function isAuthenticated(): boolean {
  return getToken() !== null
}

// getUserID decodes the `sub` claim from the stored JWT's payload, without
// verifying the signature — the backend is the only party that needs to
// verify it; the frontend only needs it to know whose id it's looking at
// (e.g. to show the "Start race" button only to the race's creator).
export function getUserID(): string | null {
  const token = getToken()
  if (!token) return null

  const payload = token.split(".")[1]
  if (!payload) return null

  try {
    const decoded = JSON.parse(atob(payload.replace(/-/g, "+").replace(/_/g, "/")))
    return typeof decoded.sub === "string" ? decoded.sub : null
  } catch {
    return null
  }
}

// getEmail decodes the `email` claim from the stored JWT's payload, the same
// way getUserID decodes `sub` — no signature verification, since this is
// only for display purposes.
export function getEmail(): string | null {
  const token = getToken()
  if (!token) return null

  const payload = token.split(".")[1]
  if (!payload) return null

  try {
    const decoded = JSON.parse(atob(payload.replace(/-/g, "+").replace(/_/g, "/")))
    return typeof decoded.email === "string" ? decoded.email : null
  } catch {
    return null
  }
}

export function logout(): void {
  clearToken()
}
