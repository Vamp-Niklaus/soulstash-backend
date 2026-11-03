# ADR-006: JSON Serialization and Parity

## Status
Accepted

## Context
Node.js JSON serialization inherently handles dynamic keys, omits `undefined` properties, preserves `null` where explicitly assigned, and formats numbers as standard IEEE floats.
In Go, `encoding/json` marshals zero-value pointers as `null` or omits them with `omitempty`, and marshals zero-value structs as empty objects.

## Decision
We use standard library **`encoding/json`** with carefully typed structs and pointers:
- Fields that Node returns as `null` (e.g. `imdb_rating: null` or `ll: null`) are modeled as pointers (`*float64`, `*[]float64`).
- Fields that Node omits when undefined use `omitempty`.
- Encoders set `SetEscapeHTML(false)` where HTML entities in URLs (like `&amp;` in TMDB or Brevo parameters) should not be converted to unicode escape sequences `\u0026`.

## Alternatives Considered
- **`sonic` or `json-iterator`**: Sonic requires AVX2 CPU instructions and CGO on certain architectures, compromising portability on ARM or non-x86 containers. Standard `encoding/json` in Go 1.22+ is fast, memory-safe, and portable.

## Consequences
### Positive
- Universal portability.
- Exact observable parity with Node JSON responses.
- Memory safe, no unsafe pointer tricks.
