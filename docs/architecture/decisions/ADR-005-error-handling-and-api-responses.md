# ADR-005: Error Handling and API Response Parity

## Status
Accepted

## Context
The Node.js server returns standard JSON error responses, primarily `{ "error": "<message>" }`, with specific HTTP status codes (400 for validation errors, 401 for missing tokens, 403 for invalid tokens / non-admins, 404 for not found, 409 for duplicate username / collection, and 500 for unexpected failures).
Observable error shapes and HTTP status codes must be preserved byte-for-byte.

## Decision
We define domain errors (`internal/domain/errors.go`) using sentinel error values:
- `ErrNotFound` -> 404
- `ErrUnauthorized` -> 401
- `ErrForbidden` -> 403
- `ErrConflict` -> 409
- `ErrBadRequest` -> 400
- `ErrInternal` -> 500

Internal service and repository errors wrap these with `fmt.Errorf("...: %w", err)`.
The HTTP presentation layer (`internal/handler`) inspects errors with `errors.Is()` or typed error interfaces and renders standard JSON:
```json
{ "error": "<human readable message>" }
```
Internal error stacks and database details are logged via `slog` with request IDs, but never leaked in HTTP responses.

## Alternatives Considered
- Direct string formatting of HTTP responses inside service layer: Violates separation of concerns.
- Panic recovery for control flow: Anti-pattern in Go, unidiomatic and slow.

## Consequences
### Positive
- Strict parity with Node.js error JSON structure and status codes.
- Full support for Go 1.13+ error wrapping (`errors.Is`, `errors.As`).
- Clean separation between business domain logic and HTTP serialization.
