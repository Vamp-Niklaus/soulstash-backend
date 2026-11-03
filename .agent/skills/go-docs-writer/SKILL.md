---
name: go-docs-writer
description: Use when creating or updating architecture, API, runbook, testing, performance, or ADR documentation for the Go backend.
---
# Go Docs Writer
Maintain in docs/:
- README.md: overview, quickstart, folder map
- architecture/overview.md (Mermaid diagrams) and architecture/decisions/
  ADR-NNN-*.md (context, decision, alternatives, consequences)
- api/openapi.yaml: generated from the parity matrix
- migration/: discovery.md, parity-matrix.md, known-quirks.md,
  assumptions.md, cutover-plan.md
- testing/README.md: how to run every test layer and add new tests/fixtures
- performance/benchmarks.md: method, baselines, results
- operations/: config-reference.md, runbook.md
- security/review.md
Rules: write for a new engineer with zero context. Every claim points to a
file or test. Update docs in the same commit as the code.
