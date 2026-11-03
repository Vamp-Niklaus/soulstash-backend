# ADR-001: HTTP Router Selection

## Status
Accepted

## Context
The Soulstash Node.js backend uses Express 5 for routing. The Go implementation requires an HTTP routing solution that:
1. Provides 100% compatibility with standard library `net/http` (`http.Handler`, `http.HandlerFunc`).
2. Supports clean sub-routing and mount points (e.g. mounting auth handlers under both `/api/auth` and `/api/user`).
3. Supports URL pattern parameters (`:id`, `:username`, `*` wildcards for SPA catchall).
4. Enables composable, idiomatic middleware chains (Request ID, Logging, CORS, Rate Limit, Panic Recovery).
5. Has minimal memory allocations and zero external runtime bloat.

## Decision
We select **`github.com/go-chi/chi/v5`** as the HTTP router.

## Alternatives Considered
- **`net/http` (Go 1.22+ ServeMux)**: While Go 1.22 enhanced `http.ServeMux` with method routing and wildcards, `chi` provides superior ergonomics for route grouping, middleware pipelines, subrouters, and route pattern introspection.
- **`github.com/gin-gonic/gin`**: Uses custom `gin.Context` handlers instead of standard `http.HandlerFunc`, introducing vendor lock-in and complicating standard library middleware integration.
- **`github.com/gofiber/fiber`**: Built on `valyala/fasthttp`, completely incompatible with `net/http`, breaks `httptest` without adapters, and introduces goroutine-safety traps with buffer reuse.

## Consequences
### Positive
- Strict compliance with `net/http` interfaces.
- Seamless compatibility with `httptest` for golden fixture testing.
- Extremely lightweight and low latency.
- Easy route duplication for parity with Node's dual mount paths (`/api/auth` and `/api/user`).

### Negative
- Slightly less built-in helper functions compared to full batteries-included frameworks like Gin, requiring thin constructor helpers.
