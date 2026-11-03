# Go Migration Standards (always apply)
- Behavioral parity with Node.js is the contract: never change API paths,
  methods, status codes, headers, JSON field names/casing, error shapes, or
  streaming formats without written approval.
- Improvements must not alter observable behavior. Node bugs go in
  docs/migration/known-quirks.md, not silently fixed.
- Ignore spa/ and everything marked out of scope. Never read or print .env*,
  .env2, or "environment".
- Layout: cmd/, internal/, pkg/ only if reusable. handler -> service ->
  repository. Constructor DI, no global mutable state.
- context.Context on all I/O, timeouts everywhere, graceful shutdown.
- Errors wrapped with %w, never swallowed, no panics in request paths.
- Structured logging with log/slog, no secrets in logs.
- Quality gates before any phase is done: go build, go vet, golangci-lint,
  go test -race ./..., gofmt.
- Docs updated in the same change as code. Small conventional commits.
- Update /PROGRESS.md after every meaningful step.
