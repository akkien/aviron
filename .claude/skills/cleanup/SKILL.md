---
name: cleanup
description: Clean up project housekeeping tasks (add "run" to execute fixes)
argument-hint: run|check
---

Review the codebase for cleanup tasks:

1. Make sure that the history in @context/current-feature.md is in order from oldest to newest
2. Find leftover debug prints — `fmt.Println`/`fmt.Printf`/bare `println` in `backend/` that should be a structured `slog` call or removed, and stray `console.log` in `frontend/src/`
3. Find unused imports — `goimports -l backend/` (or `go vet ./...`) for Go, unused imports in `frontend/src/` for TS/React
4. Check for stale TODO comments
5. Find orphaned/unused files
6. Check that context files match actual project state
7. Check that `.env.example` (or equivalent) covers the same variables the code actually reads (Postgres/Redis/Kafka DSNs, JWT secret, etc.) — flag anything referenced in code but missing from the example file, or vice versa
8. Find `//nolint` comments (Go) and `@ts-ignore`/`@ts-expect-error` comments (frontend) that might be stale
9. Run `gofmt -l backend/` and `go vet ./...` to catch formatting/vet issues the build alone wouldn't surface

**Mode: $ARGUMENTS**

If no argument or argument is "check":

- Only report findings, don't modify anything
- List what WOULD be cleaned up

If the argument is "run" or "fix":

- First, report all findings with numbered items
- Then ask: "Which items would you like me to fix? (enter numbers like 1,3,5 or 'all' or 'none')"
- Wait for user response before making any changes
- Only fix the items the user specifies
- Report what you changed
