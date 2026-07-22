import { clearToken, getToken } from "@/lib/auth"

const BASE_URL = import.meta.env.VITE_API_URL

// /auth/login and /auth/register are the only unauthenticated endpoints
// this app calls that can still return 401 — there it means "wrong
// credentials" (a normal form-validation outcome the caller already
// displays inline), not "your session expired," so they're excluded from
// the redirect-to-login behavior below.
const PUBLIC_PATHS = ["/auth/login", "/auth/register"]

export async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = getToken()
  const headers: HeadersInit = {
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...options.headers,
  }

  const res = await fetch(`${BASE_URL}${path}`, { ...options, headers })
  if (!res.ok) {
    // A 401 from any authenticated endpoint means the stored token is
    // missing/expired/invalid — every page that reaches this point already
    // assumed the user was logged in, so there's nothing useful left to
    // render. A full navigation (not react-router's navigate, which isn't
    // reachable from a plain module function outside a component) is the
    // simplest way to force back to a clean, logged-out state everywhere,
    // not just in whichever component happened to make this request.
    if (res.status === 401 && !PUBLIC_PATHS.includes(path)) {
      clearToken()
      window.location.href = "/login"
    }
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error ?? `request failed: ${res.status}`)
  }
  // 204 No Content has no body to parse.
  if (res.status === 204) return undefined as T
  return res.json()
}
