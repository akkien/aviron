# Coding Standards

## Golang

For code existing in geth, follow its style.

For new code, follow style at <https://go.dev/doc/effective_go>

### Personal naming styles

The name of constructor function: should be in format `New<StructName>`, do not name it only `New()`. For example: `NewBlock`, `NewTxPool`

Domain-layer types (`Handler`, `Service`, `Repository` interface — see "Backend Architecture" below) must be prefixed with the domain's name, not left generic, so they read clearly when used elsewhere and never stutter awkwardly when qualified (e.g. `auth.AuthHandler`, not `auth.Handler`). For the `auth` domain: `AuthHandler`, `AuthService`, `AuthRepository`, with constructors `NewAuthHandler`, `NewAuthService`. The `race` domain follows the same shape: `RaceHandler`, `RaceService`, `RaceRepository`, `NewRaceHandler`, `NewRaceService`. This also matches `internal/postgres`'s existing per-domain naming (`postgres.AuthRepository` implementing `auth.AuthRepository`).

### Backend Architecture

Every REST feature in `backend/` follows the same three domain layers, one package per feature area (e.g. `internal/auth`, `internal/race`):

- **Handler** (`internal/<domain>/handler.go`) — decodes the request, calls the service, encodes the response. No validation or business logic here. Named `<Domain>Handler` (e.g. `AuthHandler`).
- **Service** (`internal/<domain>/service.go`) — validation, orchestration, the actual business logic. Depends on the `<Domain>Repository` interface, never on a concrete DB driver. Named `<Domain>Service` (e.g. `AuthService`).
- **Repository** (`internal/<domain>/repository.go`) — defines the `<Domain>Repository` **interface** consumed by the service. Interfaces live next to their consumer (idiomatic Go), not their implementer.
- **DTOs** (`internal/<domain>/dtos.go`) — every handler's request/response structs live together in one file per domain, so the wire format is easy to scan in one place instead of scattered across handler methods.

Concrete repository implementations live in `internal/postgres/` (e.g. `internal/postgres/auth_repository.go`), one file per domain, named `<Domain>Repository` (e.g. `postgres.AuthRepository`) to match the interface it implements. A Postgres-specific repository is responsible for translating driver errors (e.g. a unique-violation `pgconn.PgError`) into domain sentinel errors defined in the domain package (e.g. `auth.ErrEmailTaken`) — nothing above the repository layer should ever import `pgx` or know the backing store is Postgres.

**Composition and routing** both live in `internal/httpserver/route.go`'s `RegisterRoutes(mux *http.ServeMux, cfg config.Config, pool *pgxpool.Pool)` — for each domain it constructs the `repository → service → handler` chain and registers the resulting handler(s) directly on the mux, which is mutated in place (no return value needed). `internal/httpserver/server.go`'s `NewServer() *http.ServeMux` only builds the empty mux and has no dependencies.

`internal/app.go` (package `internal`, imported in `main.go` under the alias `app "github.com/akkien/aviron/internal"`) is the process entrypoint: it opens the DB pool, runs migrations, builds the mux via `httpserver.NewServer()`, wires routes via `httpserver.RegisterRoutes(...)`, and serves. `cmd/server/main.go` itself only loads config and calls `app.Run(cfg)`.

Shared HTTP response helpers (`WriteJSON`, `WriteError`) live in `internal/httpx`, so handlers across domains don't duplicate response-writing boilerplate.

The primary payoff of the `<Domain>Repository` interface is testability — service-layer tests run against a fake in-memory repository instead of requiring real Postgres — not database portability; this project is committed to Postgres per context/project-overview.md §11.

## Markdown

Update on .md files must follow rules at <https://github.com/DavidAnson/markdownlint/tree/v0.40.0/doc>

Apply these rules at write time — do not write first and fix later:

- **MD032**: Always put a blank line before and after a list. Never start a list
  immediately after a paragraph, heading, or closing code fence.

  ```markdown
  <!-- wrong -->
  Some text:
  - item 1

  <!-- correct -->
  Some text:

  - item 1
  ```

- **MD024**: Headings at the same level within a document must have unique text.
  Disambiguate by appending context, e.g. "So sánh Geth vs kgeth (Phase 2)".

- **MD010**: No hard tabs — use spaces for indentation. This applies **inside
  fenced code blocks too**, including `Makefile` snippets whose real syntax
  requires tabs — indent those examples with spaces in the doc anyway; the
  actual `Makefile` file on disk (not a `.md` file) is exempt and still needs
  real tabs.

- **MD047**: File must end with a single newline character.

- **MD009**: No trailing spaces on any line.

- **MD040**: Fenced code blocks must always specify a language. Use `text` for
  pseudocode or prose snippets that don't match a real language.

  ````markdown
  <!-- wrong -->
  ```
  some pseudocode
  ```

  <!-- correct -->
  ```text
  some pseudocode
  ```
  ````

- **MD060**: Table separator row must use spaced pipes — `| --- |`, not `|---|`.
  Always write tables with spaces around every pipe:

  ```markdown
  <!-- wrong -->
  |---|---|---|

  <!-- correct -->
  | --- | --- | --- |
  ```

## TypeScript

- Strict mode enabled
- No `any` types - use proper typing or `unknown`
- Define interfaces for all props, API responses, and data models
- Use type inference where obvious, explicit types where helpful

## React

- Functional components only (no class components)
- Use hooks for state and side effects
- Keep components focused - one job per component
- Extract reusable logic into custom hooks

## Next.js

<!-- BEGIN:nextjs-agent-rules -->
### This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` before writing any code. Heed deprecation notices.
<!-- END:nextjs-agent-rules -->

- Server components by default
- Only use `'use client'` when needed (interactivity, hooks, browser APIs)
- Use Server Actions for form submissions and simple mutations
- Use API routes when you need:
  - Webhooks (Stripe, GitHub, etc.)
  - File uploads with progress tracking
  - Long-running operations
  - Specific HTTP status codes or headers
  - Endpoints for future mobile/CLI clients
  - Third-party integrations
- Otherwise, fetch data directly in server components
- Dynamic routes for item/collection pages

## Tailwind CSS v4

**CRITICAL**: We are using Tailwind CSS v4, which uses CSS-based configuration.

- **DO NOT** create `tailwind.config.ts` or `tailwind.config.js` files (those are for v3)
- All theme configuration must be done in CSS using the `@theme` directive in `src/app/globals.css`
- Use CSS custom properties for colors, spacing, etc.
- No JavaScript-based config allowed

Example v4 configuration:

```css
@import "tailwindcss";

@theme {
  --color-primary: oklch(50% 0.2 250);
}

## File Organization

- Components: `components/[feature]/ComponentName.tsx`
- Pages: `app/[route]/page.tsx`
- Server Actions: `actions/[feature].ts`
- Types: `types/[feature].ts`
- Lib/Utils: `lib/[utility].ts`

## Naming

- Components: PascalCase (`ItemCard.tsx`)
- Files: Match component name or kebab-case
- Functions: camelCase
- Constants: SCREAMING_SNAKE_CASE
- Types/Interfaces: PascalCase (no prefix)

## Styling

- Tailwind CSS for all styling
- Use shadcn/ui components where applicable
- No inline styles
- Dark mode first, light mode as option

## Database

- Use Prisma ORM for all database operations
- Always use `prisma migrate dev` for schema changes (not `db push`)
- Run `prisma migrate status` before committing to verify migrations are in sync
- Production deployments must run `prisma migrate deploy` before the app starts

## Data Fetching

- Server components fetch directly with Prisma
- Client components use Server Actions
- Validate all inputs with Zod

## Error Handling

- Use try/catch in Server Actions
- Return `{ success, data, error }` pattern from actions
- Display user-friendly error messages via toast

## Package Manager

- Use **yarn** for all package operations — never `npm install`, `npm run`, etc.
- Install packages: `yarn add <pkg>` / `yarn add -D <pkg>`
- Run scripts: `yarn <script>`

## Testing

- **Runner**: Vitest — `yarn test` (single run) or `yarn test:watch` (watch mode)
- **Scope**: server actions (`actions/**`) and utilities (`lib/**`) only — no component tests
- **Location**: co-located `__tests__/` folder next to the code under test (e.g. `lib/__tests__/utils.test.ts`)
- **Mocking**: use `vi.mock()` for Prisma, `auth()`, and other I/O dependencies in action tests
- Test files must use `.test.ts` extension (not `.spec.ts`)
- Do not use jsdom — all tests run in Node environment

## Code Quality

- No commented-out code unless specified
- No unused imports or variables
- Keep functions under 50 lines when possible
