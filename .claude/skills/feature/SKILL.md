---
name: feature
description: Manage current feature workflow - load, start, review, explain, doc or complete
argument-hint: load|start|review|explain|doc|complete
---

# Feature Workflow

Manages the full lifecycle of a feature from spec to merge.

## Working File

@context/current-feature.md

### File Structure

current-feature.md has these sections:

- `# Current Feature` - H1 heading with feature name when active
- `## Status` - Not Started | In Progress | Complete
- `## Goals` - Bullet points of what success looks like
- `## Explain` - Bullet points explaining the feature/spec (populated by `load`)
- `## Plan` - Detailed plan for implementation, including any notes on architecture, design, tradeoffs. Call out anything that diverges from the design in context/project-overview.md and why.
- `## Notes` - Additional context, constraints, or details from spec
- `## History` - Completed features (append only)

## Task

Execute the requested action: $ARGUMENTS

| Action     | Description                                               |
| ---------- | --------------------------------------------------------- |
| `load`     | Load a feature spec or inline description                 |
| `start`    | Begin implementation, create branch                       |
| `review`   | Check goals met, code quality                             |
| `explain`  | Document what changed and why                             |
| `doc`      | Document the feature for users                            |
| `complete` | Commit, push, merge, reset                                |

See [actions/](actions/) for detailed instructions.

If no action provided, explain the available options.
