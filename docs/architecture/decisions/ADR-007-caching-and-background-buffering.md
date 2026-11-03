# ADR-007: Caching and Background Telemetry Buffering

## Status
Accepted

## Context
Node.js monolith buffers traffic requests in an in-memory array (`trafficLogBuffer`) and flushes them to MongoDB every 5 seconds.
It also maintains in-memory caches for the active adult content decision tree, adult keywords list, and categories cache.

## Decision
In Go, we implement:
1. **`TrafficBuffer`**: A thread-safe bounded buffer using channels or a mutex-protected slice with a background ticker goroutine (flushing every 5 seconds or when buffer capacity is reached).
2. **Graceful Shutdown Integration**: During server shutdown (`SIGINT`/`SIGTERM`), the buffer receives a flush signal to drain all pending logs to MongoDB before process termination.
3. **`AdultDecisionCache`**: An `RWMutex`-guarded cache holding the active decision tree and keyword list in memory, synchronized with MongoDB writes.

## Alternatives Considered
- **Direct Synchronous Write per HTTP Request**: Would add 5-20ms latency to every incoming user request and overwhelm MongoDB connection pool under load.
- **External Redis / Kafka Queue**: Would introduce external infrastructure dependencies that do not exist in the Node.js application, violating parity and simplicity.

## Consequences
### Positive
- Matches Node.js asynchronous batching behavior with sub-millisecond request overhead.
- Goroutine-safe concurrent operations without race conditions (verified by `go test -race`).
- Prevents data loss during server shutdown via context cancellation and flush draining.
