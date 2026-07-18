# Start Action

1. Read current-feature.md - verify Goals are populated
2. If empty, error: "Run /feature load first"
3. Set Status to "In Progress"
4. Create and checkout the feature branch (derive name from H1 heading)
5. List the goals, then implement them one by one
6. Before wrapping up, verify once: `go build`, `go vet`, `gofmt -l .`, and `go test ./...`. This is the one full verification for the feature — `/feature complete` does not repeat the test run.

## Development Conventions

- Housekeeping steps like regenerating Swagger docs (`make docs`) are part of wrapping up implementation, not their own tracked TodoWrite item — just run them before the final verification step. Tracking every one-line housekeeping command as a separate todo costs more time than it saves.
