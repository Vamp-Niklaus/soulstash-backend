---
name: node-to-go-parity
description: Use when migrating or verifying any Node.js endpoint, middleware, or job against its Go equivalent. Ensures behavior is identical before optimizing.
---
# Node to Go Parity
1. Inventory: for each route record method, path, auth, request schema,
   response schema, status codes, headers, side effects, external calls,
   streaming behavior, and error cases.
2. Record in docs/migration/parity-matrix.md, one row per endpoint:
   Node source file:line -> Go handler -> test -> status.
3. Capture golden fixtures from the running Node app (success, validation
   error, auth error, edge cases) into testdata/golden/.
4. Implement the Go version, then replay the fixtures against the Go server
   and diff results.
5. Apply optimizations only after parity is green, and benchmark before and
   after.
6. Mark a row "verified" only with a passing test as evidence.
