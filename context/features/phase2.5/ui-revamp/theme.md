# UI Revamp — Theme

## Overview

The foundation layer for the whole revamp: fonts, color/radius/shadow tokens, applied app-wide. `dashboard.md` and `race-screen.md` both build on top of what lands here — nothing in either of those specs should need its own font-loading or base-token work.

Source: the Claude Design mockup ("Multiplayer Typing Race UI", `claude.ai/design/p/3ee82e4f-3f34-4297-ba27-7c0c6d38d885`, both `Canvas.dc.html` and `TypingRace.dc.html`) uses the same visual language throughout — Baloo 2 for headings, Poppins for body text, JetBrains Mono for typing/stat numbers, a warm oklch neutral palette, chunky 2-4px borders, large radii (12-20px), flat drop-shadow buttons (`box-shadow: 0 4px 0 <darker accent>`).

## Requirements

### Fonts

- `frontend/index.html`: add the Google Fonts `<link>` tags for Baloo 2 (weights 500/700/800), Poppins (400/500/600/700), and JetBrains Mono (500/700) — same families/weights the mockup loads.
- `frontend/src/index.css`: add `--font-heading`, `--font-sans`, `--font-mono` to the existing `@theme inline` block, so `font-heading`/`font-sans`/`font-mono` become usable Tailwind utility classes. `font-sans` becomes the new Poppins-based body default (replace whatever `body`'s current font stack resolves to); `font-mono` overrides Tailwind's built-in mono stack with JetBrains Mono, used for the typing box and stat numbers.

### Colors, radius, borders

- Extend `index.css`'s `:root` block with new warm oklch background/border tokens (the mockup's base background is `oklch(0.97 0.015 75)`, cards `oklch(0.995 0.006 80)`, borders around `oklch(0.9 0.03 80)` at 2-4px width) — additive alongside the existing `--background`/`--card`/`--border` tokens, not a wholesale replace of the token names already consumed by `components/ui/*`.
- **Keep `--primary: #3a59d1` and `--ring: #3a59d1` exactly as they are** — this was a deliberate choice made earlier in the project (see `context/current-feature.md`'s History, "Frontend Client — Login Page & Create/Join Race Page"), not something this revamp should re-litigate. It sits close enough to the mockup's own default accent (`oklch(0.58 0.19 264)`) that no visual clash results.
- **Keep `frontend/src/lib/colors.ts`'s `RACE_LANE_COLORS` palette untouched** for the same reason — also a deliberate, already-approved choice.
- Bump `--radius` up from the current `0.625rem` to something closer to the mockup's chunkier feel (12-16px range) — affects every `components/ui/*` primitive that already derives `--radius-sm`/`--radius-md`/`--radius-lg` from it, so this one token change cascades correctly without touching those files.

### `LoginPage.tsx`

- Restyle only: new fonts (`font-heading` for the "Log in" title, `font-sans` body), warm background, updated card chrome (border width/radius matching the new tokens) — so the very first screen a user sees doesn't visually clash with everything downstream.
- No logic change. No new fields, no layout restructuring beyond what the new tokens naturally produce.

## Validation

- No new client-side input validation — this spec is pure styling.

## Data

- No new types.

## Notes

- Depends on nothing — this is the first spec in Phase 2.5's build order.
- `dashboard.md` and `race-screen.md` both depend on this landing first (see `phase-2.5-plan.md`'s Dependency order).
- No backend changes.
