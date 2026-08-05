# Complete Action

1. Stage all changes and commit with a descriptive message. Never put "Generated With Claude" in the commit messages
2. Switch to main and merge the feature branch (no push yet)
3. Delete the local feature branch
4. Reset current-feature.md:
   - Change H1 back to `# Current Feature`
   - Clear Goals, Explain, Plan, and Notes sections (keep placeholder comments)
5. Append the feature summary to the bottom of the entries in
   context/feature-history.md (oldest first, reordered 2026-08-05) —
   current-feature.md no longer has a History section of its own (moved
   out 2026-07-26 once it grew large enough to hit read-truncation
   limits)
6. Commit both files together: `chore: reset current-feature.md after completing [feature]`
7. Ask user for confirmation before pushing, then push main to origin ONCE (single push with all changes)
8. If feature branch was previously pushed, delete it from origin

## Development Conventions

- Do NOT re-run `go test ./... -race` here — `/feature start` already verified the full suite once during implementation, and repeating it is redundant and costly (e.g. the bcrypt-heavy auth tests alone take 20-45+ seconds). If code changed after that verification, re-verify only what changed, not the whole suite by default.
