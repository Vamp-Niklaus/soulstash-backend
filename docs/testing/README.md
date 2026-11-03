# Soulstash Go Backend Testing Guide

## 1. Overview & Strategy

Testing strategy adheres to `.agent/skills/go-testing-strategy/SKILL.md`:
- **Unit & Business Logic**: Table-driven tests (`t.Parallel()`), testing business logic in `internal/service/` and `internal/domain/`.
- **Contract & Parity Tests**: Golden fixture replays asserting exact JSON responses, HTTP status codes, and error formatting (`internal/handler/auth_handler_test.go`, `content_handler_test.go`).
- **Failure Injection**: Testing upstream timeouts, malformed JSON bodies, and database connectivity drops (`internal/handler/failure_injection_test.go`).
- **Concurrency & Race Detection**: Executed via `go test -race ./...` (delegated to Linux CI runner).
- **Micro-Benchmarks**: Memory allocation and throughput profiling via `testing.B -benchmem` in `internal/service/`.
- **Load Testing**: k6 and Vegeta automated scenarios in `loadtest/`.

---

## 2. Test Commands Reference

All test workflows are automated through the root `Makefile`:

```bash
# 1. Run all unit and contract tests with verbose output
make test

# 2. Run tests with race detection (Linux / CI runner)
make test-race

# 3. Generate test coverage profile and terminal summary
make cover

# 4. Run micro-benchmarks with memory allocation profiling
make bench

# 5. Run static code analysis with golangci-lint
make lint

# 6. Format all code according to Go standards
make fmt

# 7. Run load test against local or staging environment
make load
```

---

## 3. Directory Layout of Test Suites

```text
backend-go/
├── internal/
│   ├── config/
│   │   └── config_test.go            # Validation and fail-fast startup tests
│   ├── handler/
│   │   ├── auth_handler_test.go       # Auth register, login, OTP, profile contract tests
│   │   ├── collection_handler_test.go # Collection CRUD and optimistic locking tests
│   │   ├── content_handler_test.go    # Content detail and combined genres tests
│   │   ├── admin_handler_test.go      # Admin auth protection and decision tree tests
│   │   └── failure_injection_test.go  # Malformed JSON, 504 gateway timeout tests
│   ├── middleware/
│   │   └── middleware_test.go         # RequestID, Recoverer, JWT, Rate Limiter tests
│   ├── server/
│   │   └── server_test.go             # Health, Readiness, Image Proxy, 404 tests
│   └── service/
│       ├── adult_tree_test.go         # Adult decision engine evaluation tests
│       ├── adult_tree_bench_test.go   # Adult tree traversal micro-benchmarks
│       ├── auth_service_test.go       # Auth business logic and bcrypt tests
│       ├── collection_service_test.go # Collection mutation and versioning tests
│       ├── content_scoring_test.go    # Relevance scoring and media normalization tests
│       ├── scoring_bench_test.go      # Scoring algorithm micro-benchmarks
│       └── ratings_service_test.go    # IMDb 10.0 sentinel filter tests
├── testdata/
│   └── golden/
│       └── auth/                      # Captured golden fixtures from Node.js
└── loadtest/
    ├── k6_scenario.js                 # k6 load testing script
    └── vegeta_targets.txt             # Vegeta attack targets
```

---

## 4. Capturing & Updating Golden Fixtures

When introducing a new endpoint or verifying parity against Node.js:
1. Capture the raw JSON response from Node.js:
   ```bash
   curl -s http://localhost:5000/api/auth/check-username?username=cooluser > testdata/golden/auth/check_username_available.json
   ```
2. In the Go contract test (`internal/handler/*_test.go`), load the fixture using `os.ReadFile` and compare with `httptest.ResponseRecorder.Body`.
3. Update `docs/migration/parity-matrix.md` to reflect the verified status.
