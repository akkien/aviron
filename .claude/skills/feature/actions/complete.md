# Complete Action

1. Stage all changes and commit with a descriptive message. Never put "Generated With Claude" in the commit messages
2. Switch to main and merge the feature branch (no push yet)
3. Delete the local feature branch
4. Reset current-feature.md:
   - Change H1 back to `# Current Feature`
   - Clear Goals and Notes sections (keep placeholder comments)
   - Add feature summary to the END of History
5. Commit the reset: `chore: reset current-feature.md after completing [feature]`
6. Ask user for confirmation before pushing, then push main to origin ONCE (single push with all changes)
7. If feature branch was previously pushed, delete it from origin

## Development Conventions

- Do NOT re-run `go test ./... -race` here — `/feature start` already verified the full suite once during implementation, and repeating it is redundant and costly (e.g. the bcrypt-heavy auth tests alone take 20-45+ seconds). If code changed after that verification, re-verify only what changed, not the whole suite by default.
