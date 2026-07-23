# Doc Action

1. Create new section in /docs/feature-log.md
2. Add documentation for the feature we just done. Each section should have a sentence summarizing the feature and 2 sub sections:

- Goals: as what we have in `## Goals` of /context/current-feature.md
- Explain: how we design & implement the feature, trade offs we made, etc.

3. Every feature's Explain subsection must include at least one visual aid beyond prose — pick whichever fits the feature best:
   - **Code sample** — a Go struct/signature, JSON wire message, SQL query, or curl request/response. For a REST endpoint, a request/response pair is usually the clearest choice; for a data shape, the actual struct/type.
   - **Table** — comparing options, listing status/error codes, mapping types to examples (e.g. Prometheus metric types), a before/after values table.
   - **Diagram** (` ```mermaid ` fenced block) — a `sequenceDiagram` for a request/message flow or for walking through a race-condition bug, a `flowchart` for a decision/architecture/select-loop, a `stateDiagram-v2` for a lifecycle (e.g. race status, connection state). Reach for a diagram specifically when the feature is about *sequencing*, *flow*, or *state transitions* — that's what prose explains worst and a diagram explains best.

   Place the visual right next to the bullet it illustrates, not bolted on at the end. Skip a diagram in favor of a code sample or table when nothing about the feature is sequence/flow/state-shaped enough to justify one — don't force a diagram where a two-line code snippet says the same thing faster.

4. Any code/JSON/SQL sample shown must reflect the real, current implementation — read the actual source file(s) (or run the actual command, e.g. to capture real log/metrics output) before writing it into the doc. Don't reconstruct a plausible-looking example from memory only; verify it.
