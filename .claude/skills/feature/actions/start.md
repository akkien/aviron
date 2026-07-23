# Start Action

1. Read current-feature.md - verify Goals are populated
2. If empty, error: "Run /feature load first"
3. Set Status to "In Progress"
4. Create and checkout the feature branch (derive name from H1 heading)
5. List the goals, then implement them one by one
6. Before wrapping up, verify once: `go build` and `go test ./...`. This is the one full verification for the feature — `/feature complete` does not repeat the test run.

## Development Conventions

- Do **not** run `go vet`, `gofmt`/`gofmt -l .`, or `make docs` (Swagger regen) as part of implementing a feature — the user runs these themselves when they want. Don't add them to verification steps or TodoWrite items.
