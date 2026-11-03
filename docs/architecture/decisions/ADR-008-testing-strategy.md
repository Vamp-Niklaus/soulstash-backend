# ADR-008: Testing Strategy and Quality Gates

## Status
Accepted

## Context
Production readiness demands verification of 95 endpoints, concurrent race-safety, graceful shutdown, and zero behavioral regressions compared to the Node.js baseline.

## Decision
We enforce a multi-tiered testing strategy:
1. **Unit Tests**: Table-driven tests using `testing.T` and `t.Parallel()`. Interfaces mocked at boundaries.
2. **Contract Golden Tests**: In `testdata/golden/`, capture and replay input/output JSON fixtures comparing Go HTTP handlers directly against expected Node responses via `net/http/httptest`.
3. **Integration Tests**: Under build tag `integration`, execute tests against containerized dependencies (MongoDB).
4. **Race Condition Checks**: Enforce `go test -race ./...` on all PRs and CI runs.
5. **Quality Gates**: Every change must pass `gofmt`, `go vet`, `go test -race ./...`, and `go build ./...`.

## Alternatives Considered
- Relying solely on manual API testing: Rejected; lacks repeatability and auditability.
- End-to-end testing requiring live internet connection to TMDB/Brevo: Flaky, subject to rate limits. Mocks and recorded fixtures used for deterministic builds.

## Consequences
### Positive
- Strict audit trail with passing automated test evidence for every endpoint in the parity matrix.
- Clean separation between fast local unit tests and slower integration tests.
- High test coverage (target: 80%+ overall, 90%+ on service/domain logic).
