# ADR-003: Configuration Management and Fail-Fast Validation

## Status
Accepted

## Context
The application relies on 14 environment variables (`MONGODB_URI`, `MONGODB_DB_NAME`, `JWT_SECRET`, `BREVO_API_KEY`, `TMDB_BEARER_TOKEN`, etc.).
Missing required variables in production leads to silent failures or late runtime errors.
In development, loading from a local `.env` file is standard convenience.

## Decision
We implement a typed configuration struct (`internal/config.Config`) with:
1. `github.com/joho/godotenv` to optionally load `.env` if present in development without failing if missing.
2. An explicit parser mapping environment variables with safe defaults.
3. A `Validate()` method that runs immediately on startup and fails fast with actionable error messages if required variables are absent or invalid.

## Alternatives Considered
- **`spf13/viper`**: Overly complex, pulls in heavy transitive dependencies (yaml, toml, consul, etc.), unnecessary for pure environment-variable-driven services.
- **Global `os.Getenv()` calls scattered across code**: Unsafe, impossible to unit test deterministically, causes hidden bugs.

## Consequences
### Positive
- Centralized configuration in a single immutable struct.
- Constructor dependency injection passes `*config.Config` explicitly into components.
- Fails immediately at boot time if configuration is invalid.
