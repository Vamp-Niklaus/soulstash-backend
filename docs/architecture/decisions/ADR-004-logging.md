# ADR-004: Structured Logging with `log/slog`

## Status
Accepted

## Context
Production logging requires:
1. Structured key-value logging for machine readability (e.g. JSON in production, human text in local dev).
2. Traceability via contextual `request_id` across goroutines and middleware.
3. Zero risk of leaking sensitive secrets (JWT tokens, passwords, API keys) into log sinks.
4. High throughput with zero or minimal heap allocations.

## Decision
We use Go's standard library **`log/slog`** package.
In production (`APP_ENV=production`), logs are output as JSON (`slog.NewJSONHandler(os.Stdout, ...)`).
In development, logs use formatted text (`slog.NewTextHandler(os.Stdout, ...)`).
A custom middleware extracts or generates a UUID request ID, injects it into `context.Context`, and attaches a child logger with `request_id` to the request context.

## Alternatives Considered
- **`uber-go/zap`**: High performance but adds a third-party dependency when Go 1.21+ has standard `log/slog`.
- **`rs/zerolog`**: Excellent performance but non-standard API.
- **Node's patched `console.log`**: Unstructured, non-standard, and slow under high concurrency.

## Consequences
### Positive
- Zero external dependencies.
- Native standard library support.
- Safe structured attributes preventing log injection.
