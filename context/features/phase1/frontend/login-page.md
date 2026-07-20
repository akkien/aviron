# Login Page

## Overview

The first screen of the React test client: a plain login form that calls the backend's `/auth/login` and stores the JWT for subsequent requests. This exists purely to exercise the backend, per the project's "no need to polish UI/UX" scope.

## Requirements

### Page

- Route: `/login`
- Fields: email, password
- Submit calls `POST /auth/login`
- On success: store the JWT (React state + `localStorage`, so a page refresh doesn't force re-login) and redirect to `/races`
- On failure: show the error message inline — no toast library needed

### No Registration UI

- Registration is exercised via curl/Postman for now, not a form — keeps this feature small; add a register form later only if manual testing gets tedious

## Validation

- Client-side: disable submit while either field is empty (basic UX guard, not a substitute for the server-side validation in User Registration/Login)

## Data

- Plain `fetch` to the backend's `/auth/login` — no data-fetching library needed for a client this small

## Notes

- First frontend feature to touch the project, so this is also where Tailwind CSS + shadcn/ui get scaffolded: `npx shadcn init` (Vite preset) sets up Tailwind v4 (CSS-based `@theme` config, no `tailwind.config.ts` — see context/coding-standards.md) and the shadcn CLI/component registry
- Build the form from shadcn's `Input`, `Label`, and `Button` primitives instead of hand-rolled `<input>`/`<button>` elements — still a single `LoginPage.tsx` with controlled inputs, just faster and more consistent than custom CSS; not a polish investment
- Base API URL comes from a Vite env var (`VITE_API_URL`), not hardcoded
