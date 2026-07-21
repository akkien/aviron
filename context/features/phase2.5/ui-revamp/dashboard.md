# UI Revamp — Dashboard

## Overview

The pre-race state of `RacesPage` — everything a user sees before they've created or joined a race. Adapts the mockup's `Canvas.dc.html`: a top bar (logo, user, logout), 4 stat cards, a "Create a Race" form, and a browsable "Open Races" list. Depends on `theme.md` already being in place.

Two things in the mockup have no backing data today and are explicitly built as hardcoded placeholders per the scope decision recorded in `phase-2.5-plan.md`: the stat cards, and the Open Races list. Everything else here is real.

## Requirements

### `AppHeader.tsx` (new, `frontend/src/components/layout/`)

- Top bar: our actual "Aviron" branding (not the mockup's placeholder "SprintType" — this app already has a name), a user avatar (initials, same visual treatment as the mockup's colored-circle avatar) and the user's email, and a real "Log Out" button.
- **This is the first logout functionality anywhere in the app.** `frontend/src/lib/auth.ts` already has `clearToken()` but nothing calls it. Add a small `logout()` helper there (`clearToken` + the caller navigates to `/login`) and wire the header's button to it.
- The header needs something to display for the user — `getUserID()` already decodes the JWT's `sub` claim; add a parallel `getEmail()` decoding the JWT's existing `email` claim (already present on every login-issued token, see `internal/auth/service.go`'s claims) the same way. Avatar initials derive from the email's local part (e.g. first two letters before `@`) since there's no display-name claim on the token.
- Rendered only on the Dashboard state (and, once `race-screen.md` lands, presumably not on the full-height race screen — that spec owns its own layout). Not rendered on `LoginPage`.

### `StatCards.tsx` (new, `frontend/src/components/races/`)

- 3 cards: Races Joined, Races Won, Avg WPM. The mockup's 4th card (Avg Accuracy) is dropped outright, not built at all — it turns out not to be measurable under this project's never-verify-typed-text trust model (see `user-stats/user-stats.md`'s Overview), so there is nothing for that spec to later remove.
- **Hardcoded values.** No API call. Comment directly in the component noting this is a placeholder pending `user-stats/user-stats.md` (already spec'd — that feature replaces all 3 cards with real data). Structure the hardcoded values as a single object/array passed into the card-rendering markup (not inlined literally per-JSX-element) so swapping the data source later is a small, contained diff.

### `OpenRacesList.tsx` (new, `frontend/src/components/races/`)

- A hardcoded seed list (mirrors the mockup's own `RACE_SEED` shape: name, host, word count, player count/max, a lane color) rendered as a card with a "Join" button per row.
- Reuse the mockup's own already-implemented interaction: clicking Join increments that row's local player count client-side (capped at max) — clearly decorative, not a real join, since these aren't real races. Comment this explicitly.
- This does **not** replace the real join flow below — it's a visual placeholder for a future real "browse open races" feature.

### `CreateRaceForm.tsx` / `JoinRaceForm.tsx`

- Restyle only — same real `POST /races` / `POST /races/{id}/join` calls, same props/callbacks into `RacesPage`. No logic changes.
- The mockup's Dashboard has no join-by-id field at all (it assumes discovery only through the fake Open Races list). Since `JoinRaceForm` is the only *real* way to join a specific race today, it stays — reposition it near `OpenRacesList` (e.g. a "Have a race ID?" affordance) rather than dropping it.

### `RacesPage.tsx`

- Dashboard-state layout: `AppHeader` at top, `StatCards` row, then a two-column area with `CreateRaceForm` + `JoinRaceForm` on one side and `OpenRacesList` on the other — matching the mockup's arrangement.
- This only covers the pre-race state. Once a race exists (`raceId` set), `race-screen.md` takes over the layout entirely — that transition is this spec's boundary, not something to half-implement here.

## Validation

- `CreateRaceForm`: unchanged (word-count-positive check already exists).
- `OpenRacesList`'s Join button: no validation needed, it's a local counter increment with a max cap.

## Data

- No new backend types. `OpenRacesList`'s seed data and `StatCards`' values are local constants, not fetched.

## Notes

- Depends on `theme.md` (fonts/tokens) already landing.
- Independent of `race-screen.md` — the two states share only the theme layer (see `phase-2.5-plan.md`'s Dependency order).
- No backend changes.
