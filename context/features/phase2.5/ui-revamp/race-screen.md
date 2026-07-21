# UI Revamp — Race Screen

## Overview

Replaces the current stacked `RaceStatusView` + `TypingView` cards with an immersive full-height 30/70 layout, adapted from the mockup's `TypingRace.dc.html`: a left sidebar whose content depends on race status, and a right panel with an animated horizontal track — one lane per participant, a vehicle emoji sliding along a progress bar toward a checkered finish line.

Depends on `theme.md` already landing. Independent of `dashboard.md` — this spec owns the full-height layout shown once a race exists (`raceId` set on `RacesPage`), taking over from the Dashboard state entirely rather than being embedded inside it.

**No changes to `useRaceSocket.ts`'s logic, and no backend changes.** Every field it already exposes (`raceState`, `finished`, `connectionError`, `leaving`, `reconnecting`, `evicted`, `sendTelemetry`, `leaveRace`) stays exactly as-is, and the WebSocket/REST wire shapes are unchanged. One requirement below (the typing box's word validation) is a real *client-side* interaction-logic change, not pure styling — see that section for why it still fits inside "no backend changes."

Refined against the actual prompt given to Claude Design to produce this mockup (verbatim, kept for reference — describes a desktop-first 30/70 split, a pre-race sidebar with a player list + cosmetic vehicle picker, an in-race sidebar with a strict 10fastfingers-style typing box, and a dynamic-lane-count animated track): the prompt confirmed most of what was already captured here and added one requirement this spec's first draft missed — the typing box must **block advancement on an incorrect word**, not just diff-color it (see below).

## Requirements

### Layout shell

- Full-height (`100vh`/`min-height:700px` per the mockup), two panels: left sidebar ~30% width (min ~340px), right track panel ~70%.
- Race name header, and a status indicator badge (matches the mockup's pill-shaped status label).

### Left sidebar — pending state (race not yet `active`)

- Race name, participant list (today's `RaceStatusView` participant list, restyled — name + joined time), each row with a color swatch matching that player's lane color on the track. Both the sidebar list and the track panel must derive a player's color from the same source (today's `laneColor(index)` in `lib/colors.ts`, keyed by the same participant ordering in both places) so the two panels never disagree about which color belongs to which player.
- **Vehicle picker** (new): 4 options (car/bike/rocket/van, matching the mockup's `VEHICLES`). New `frontend/src/lib/vehicles.ts`:
  - `VEHICLES` — the 4 options with emoji + label.
  - `vehicleForUser(userId: string): Vehicle` — a deterministic hash of `userId` into the array. Every viewer computes this the same way, so a given opponent's icon looks identical across every client without any server sync (per the scope decision in `phase-2.5-plan.md` — vehicles are purely cosmetic, the backend never sees them).
  - The local player's own explicit pick from the sidebar picker overrides `vehicleForUser(currentUserId)` for their own rendering only — kept in local component state (not persisted, not sent anywhere), the same as the mockup's own `selectVehicle`.
- Start Race button (creator-only, existing `POST /races/{id}/start` — no change to `RaceStatusView`'s existing `handleStart`, just restyled).
- Leave button (existing `POST /races/{id}/leave` from `leave-race/leave-race.md` — the pre-start leave path).
- Race ID + Copy button (from the earlier "show race id" addition) — carries over into this new layout unchanged in behavior.

### Left sidebar — active state (race `active`, not finished/left/evicted)

- Condensed leaderboard: ranked list derived from the latest `race_state` tick (rank, colored dot, name, mini progress bar, percentage) — matches the mockup's leaderboard block, driven by real `raceState.participants` instead of mock data. Kept deliberately small/secondary during racing — see Visual hierarchy below.
- **Word-by-word typing box**, replacing the current plain textarea + typed/remaining text split. Behavior confirmed by the Claude Design prompt (10fastfingers-style, stricter than this spec's first draft):
  - Completed words rendered in green, permanently — once a word is correctly completed it doesn't change state again.
  - **The current word gates advancement on correctness — this is a real change to `TypingView.tsx`'s interaction logic, not just a rendering diff.** Today's `countCompletedWords` treats any whitespace-separated token as "done," regardless of whether it matches the prompt; that's no longer sufficient. The current word must be checked against `promptText`'s corresponding word on every keystroke:
    - Characters typed so far are diffed against the target word (correct-so-far styled one way, a mismatch styled as an error/flagged state) as the mockup's own reference implementation already does (`TypingRace.dc.html`'s `onInputChange`/`onKeyDown`: an input that stops matching the target word's prefix sets an error flag; pressing space only advances the cursor if the typed word exactly equals the target word, otherwise the error flag is set and the cursor stays put).
    - The player cannot move to the next word — no telemetry is sent for it — until the current word is corrected to exactly match. Only a correctly-typed word advances.
    - This validation is **entirely client-side**, same as it is today: the server still never inspects typed text (`context/project-overview.md` §13's trust model is unchanged) — `telemetry` still just reports a word-completed count once the client itself has decided a word is done. Making the client stricter about *when* it decides a word is done doesn't touch the wire protocol.
  - Pending words in a muted gray, blinking cursor.
  - The hidden/positioned `<input>` trick from the mockup (visually-hidden real input, styled fake caret) is one valid approach — implementation detail to resolve in `start`, as long as the validation behavior above and the actual telemetry-sending behavior (one `telemetry` message per correctly-completed word, via `sendTelemetry`) both hold.
- Quit Race button — same `leaveRace()` call already wired, restyled.

### Visual hierarchy

Per the Claude Design prompt's explicit direction: this should read as "an energetic, competitive, game-like racing HUD, not a plain corporate dashboard." During the active-race state specifically, the typing box and the track panel are the two elements that should dominate visual attention — the condensed leaderboard and race-info header stay small/secondary by comparison, not competing for focus.

### Right panel — track (present whenever a race exists, any status)

- One lane per participant, **dynamically sized to however many players are actually in the race** (2 up to `MaxParticipants = 10`, per `internal/race`'s existing cap — not a fixed lane count): vehicle emoji (per the vehicle picker above) positioned via `left: {distance_m / distanceMeters}%`, transitioning smoothly (CSS transition, not a jump) on each `race_state` tick (matches the mockup's `trackPlayers` block, driven by real data instead of its mock `progress`).
- Player name tag above/near each vehicle.
- Start marker, dashed lane guide, checkered finish marker — cosmetic, no data dependency.

### Other states — restyled, no new interaction logic

None of these exist in the source mockup (it never modeled reconnection, eviction, or a finished/results screen), so there's no mockup to match — restyle consistently with the new visual language (fonts, colors, chunky borders) rather than leaving Phase 2's plain-text versions as-is:

- **Reconnecting** (`reconnecting` true): today's inline "Reconnecting..." text, restyled — still non-blocking, sidebar/track keep showing last-known state per `reconnect-ui.md`'s original requirement.
- **Evicted** (`evicted` true): today's "You were disconnected too long and have left the race" message, restyled as its own sidebar state.
- **Leaving** (`leaving` true): today's "You left the race" message, restyled.
- **Finished** (`finished` set): today's ranked results list (rank, name, time or "DNF" for `finish_time_ms: null`), restyled — reasonable to keep this in the sidebar rather than the full two-panel layout, since the race is over and the track no longer needs to be the focus.

## Validation

- **New**: the typing box's word-correctness gate (see above) — the current word must exactly match `promptText`'s corresponding word before the cursor/telemetry advances past it. This is the one piece of real interaction logic this spec adds; everything else is styling.
- No new *server-side* validation — unchanged from today, per project-overview.md §13.

## Data

- No new backend types, no new WebSocket message shapes.
- New local-only type in `lib/vehicles.ts`: `Vehicle { id: string; label: string; emoji: string }`.

## Notes

- Depends on `theme.md` (fonts/tokens) already landing.
- Depends on `frontend-realtime/websocket-client.md` and `frontend-realtime/reconnect-ui.md` (both done) — this spec restyles their output, doesn't change their logic.
- Depends on `leave-race/leave-race.md` (done) for the pre-start Leave button and the `leave_race` Quit Race behavior already wired.
- No backend changes.
