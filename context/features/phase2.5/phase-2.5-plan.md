# Phase 2.5 — UI Revamp Plan

## Overview

Not part of `context/project-overview.md`'s original roadmap — inserted between Phase 2 (real-time core, now fully complete) and Phase 3 (production-readiness) by explicit request. Phase 1's frontend work got the app functional but gave it zero visual identity beyond shadcn's defaults. This phase imports a Claude Design mockup ("Multiplayer Typing Race UI", `claude.ai/design/p/3ee82e4f-3f34-4297-ba27-7c0c6d38d885`, pulled via the `DesignSync` MCP tool) and adapts its visual language — fonts, colors, chunky borders/shadows, an animated car-race track — onto the app's existing real data flows. `ui-revamp/race-screen.md` was later refined against the actual prompt given to Claude Design to produce that mockup, once the user shared it — it confirmed most of what had already been captured and added one real behavioral requirement (strict must-correct-before-advancing typing validation, 10fastfingers-style) that the first draft had missed.

This started as a **pure frontend re-skin plus one structural layout change** (splitting `RacesPage` into a Dashboard state and an immersive Race Screen state). It still mostly is — `useRaceSocket.ts`'s connection/reconnect logic and every WebSocket/REST message shape stay untouched — with two additions beyond pure styling: `race-screen.md`'s client-side typing-validation logic (still no backend/wire-protocol change), and `user-stats/user-stats.md` (below), which is real backend work.

Three scope decisions were made with the user before/while writing these specs, since the mockup assumes capabilities that don't exist yet:

- **Vehicles (car/bike/rocket/van icons) are purely cosmetic.** The room actor never broadcasts a vehicle field — only `user_id`/`distance_m`/`rank`. A player's own vehicle choice is local-only, not synced; every viewer renders a deterministic vehicle per `user_id` for anyone whose real choice they can't know, so opponents still look consistent across viewers without any server involvement.
- **The mockup's "Open Races" browsable list has no backing endpoint** (no `GET /races` list exists). Per explicit request: **built now with hardcoded data** in `dashboard.md`; a real browsable-list endpoint is separate, later work, still out of scope. The real "Create Race" and "Join by ID" flows stay fully real.
- **The mockup's 4 stat cards started the same way (no backing data) but changed scope mid-phase**: the user asked for real backend support now rather than staying hardcoded, which is what `user-stats/user-stats.md` builds — 3 of the 4 cards (Races Joined/Won/Avg WPM) become real; the 4th (Avg Accuracy) is dropped outright, not built, since the server's never-verify-typed-text trust model (`project-overview.md` §13) leaves nothing server-side to average without a new self-reported client metric, which was explicitly declined.

## Specs, in build order

1. `ui-revamp/theme.md` — fonts, Tailwind theme tokens, `LoginPage` restyle. Everything else depends on this landing first.
2. `ui-revamp/dashboard.md` — the pre-race Dashboard state: header/logout, hardcoded stat cards (see spec 4 below), hardcoded Open Races list, restyled create/join forms.
3. `ui-revamp/race-screen.md` — the in-race Dashboard state: 30/70 sidebar+track layout, vehicle picker, strict word-validation typing box, animated track, restyled reconnect/evicted/leaving/finished states.
4. `user-stats/user-stats.md` — backend support (new `internal/leaderboard` domain, one migration, a fix to a pre-existing `AvgPaceWatt` gap) plus wiring `StatCards.tsx` to real data, dropping the Avg Accuracy card. Mostly backend, unlike the other three specs.

## Dependency order

- `dashboard.md` and `race-screen.md` both depend on `theme.md`'s tokens (fonts, colors, radii) already existing — building either first would mean redoing their styling once the theme lands.
- `race-screen.md` depends on nothing from `dashboard.md` — the two states are independent screens sharing only the theme layer, so their build order relative to each other doesn't matter, though `dashboard.md` is listed first since it's what a user sees immediately after login.
- `user-stats.md` depends on `dashboard.md`'s `StatCards.tsx` already existing (it replaces that component's data source, doesn't build it) — the backend portions of `user-stats.md` (migration, `internal/leaderboard`, the `AvgPaceWatt` fix) have no frontend dependency and could technically be built earlier, but the spec is sequenced last since its frontend half needs `dashboard.md` done first.

## Explicitly out of scope for Phase 2.5

- A real `GET /races` browsable-list endpoint — `dashboard.md`'s Open Races list stays hardcoded/decorative.
- A real synced vehicle field — stays purely cosmetic/client-side per the decision above.
- The Avg Accuracy stat — dropped, not deferred; would require a new self-reported client metric that was explicitly declined.
- The full ranked/windowed `GET /leaderboard?window=alltime|weekly` from `project-overview.md`'s original API surface — `user-stats.md` only builds `GET /leaderboard/me` (the caller's own stats), not a public ranked list.
- Redesigning `LoginPage.tsx` beyond applying the new theme tokens (no mockup was provided for it).
