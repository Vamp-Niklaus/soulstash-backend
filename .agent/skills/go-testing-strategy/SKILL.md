---
name: go-testing-strategy
description: Use when writing or reviewing tests for the Go backend, including unit, integration, contract, race, benchmark, and load tests.
---
# Go Testing Strategy
- Unit: table-driven tests, t.Parallel(), interfaces mocked at boundaries.
- Integration: real dependencies via testcontainers-go or docker-compose,
  behind the build tag `integration`.
- Contract: replay golden fixtures from Node against the Go server with
  httptest.
- Concurrency: run with -race; test cancellation, timeouts, and shutdown.
- Failure paths: upstream timeouts, malformed input, partial streams, DB down,
  client disconnect mid-stream.
- Benchmarks: testing.B with -benchmem on hot paths; record Node baseline vs
  Go in docs/performance/benchmarks.md.
- Load: k6 or vegeta scenarios in loadtest/, reporting p50/p95/p99, RPS, and
  error rate.
- Coverage: 80%+ overall, 90%+ on service and domain logic.
- Makefile targets: test, test-integration, test-race, bench, lint, cover,
  load.
