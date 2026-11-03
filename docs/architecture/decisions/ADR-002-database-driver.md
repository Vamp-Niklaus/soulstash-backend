# ADR-002: Database Driver & Access Pattern

## Status
Accepted

## Context
The Soulstash backend uses MongoDB for multiple collections: `users`, `Ratings`, `PlayerSources`, `SourceConfigs`, `AppMetrics`, `TrafficLogsMonolith`, `admin_setting_monolith`, and `password_resets`.
Queries involve atomic update operators (`$push`, `$set`, `$inc`, `$setOnInsert`), array index filters, complex aggregation pipelines (traffic statistics), TTL indexes, and unique constraints.

## Decision
We select the official **`go.mongodb.org/mongo-driver/v2`** (or `v1` if required by ecosystem tools) and adopt a clean **Repository pattern without an ORM**.

## Alternatives Considered
- **Third-party ORMs / ODMs (e.g., `kamva/mgm`)**: Introduce leaky abstractions, complicate advanced aggregation pipelines, and add reflection overhead without substantial benefit.
- **Relational SQL DB Migration**: Rejected because non-negotiable parity rule dictates exact preservation of existing data models, document structures, and MongoDB collections without disrupting data.

## Consequences
### Positive
- Direct access to MongoDB aggregation pipelines and atomic update operators.
- Full control over connection pooling, timeouts, and BSON serialization.
- Fully decoupled repositories with interfaces defined at the consumer service layer, allowing simple mock injection during unit tests.

### Negative
- Requires explicit struct BSON tags and mapping helpers for domain entities.
