# System Architecture & Layered Design

## Overview
The Soulstash Go backend is engineered according to Clean/Hexagonal Architecture principles. Dependencies point strictly inwards:
`HTTP / Transport Layer` $\rightarrow$ `Service / Business Logic Layer` $\rightarrow$ `Repository / Data Layer` $\rightarrow$ `Domain Entities`.

```mermaid
flowchart TD
    Client["Client (Web Browser / SPA / Mobile)"]

    subgraph Transport["Transport Layer (internal/server, internal/handler, internal/middleware)"]
        Router["chi Router"]
        MW["Middleware Chain (RequestID, Slog, CORS, RateLimit, Recover)"]
        Handlers["HTTP Handlers (Auth, Content, Collections, Admin, Pages)"]
    end

    subgraph ServiceLayer["Service Layer (internal/service)"]
        AuthSvc["Auth Service"]
        ContentSvc["Content Service"]
        CollectionSvc["Collection Service"]
        AdminSvc["Admin Service"]
        ScraperSvc["Scraper Service"]
    end

    subgraph RepositoryLayer["Repository Layer (internal/repository)"]
        UserRepo["User Repository (MongoDB)"]
        RatingsRepo["Ratings Repository (MongoDB)"]
        SourcesRepo["Player Sources Repository (MongoDB)"]
        TrafficRepo["Traffic Logs Repository (MongoDB)"]
        SettingsRepo["Admin Settings Repository (MongoDB)"]
    end

    subgraph ExternalClients["External Clients (internal/platform)"]
        TMDBClient["TMDB HTTP Client"]
        OMDbClient["OMDb HTTP Client"]
        BrevoClient["Brevo Email Client"]
        WatchmodeClient["Watchmode Client"]
    end

    subgraph DomainLayer["Domain Layer (internal/domain)"]
        Models["Models, Entities, Enums, Sentinel Errors"]
    end

    Client --> Router
    Router --> MW
    MW --> Handlers
    Handlers --> AuthSvc
    Handlers --> ContentSvc
    Handlers --> CollectionSvc
    Handlers --> AdminSvc
    Handlers --> ScraperSvc

    AuthSvc --> UserRepo
    AuthSvc --> BrevoClient
    ContentSvc --> RatingsRepo
    ContentSvc --> SourcesRepo
    ContentSvc --> TMDBClient
    ContentSvc --> OMDbClient
    CollectionSvc --> UserRepo
    AdminSvc --> UserRepo
    AdminSvc --> TrafficRepo
    AdminSvc --> SettingsRepo

    UserRepo --> DomainLayer
    RatingsRepo --> DomainLayer
    SourcesRepo --> DomainLayer
    TrafficRepo --> DomainLayer
    SettingsRepo --> DomainLayer
```

---

## Request Lifecycle

Every HTTP request follows a deterministic pipeline with structured logging, distributed tracing, and panic recovery:

```mermaid
sequenceDiagram
    autonumber
    actor User as Client
    participant MW as Middleware Chain
    participant H as Handler
    participant S as Service
    participant R as Repository
    participant DB as MongoDB / External API

    User->>MW: HTTP Request (Method, Path, Headers)
    MW->>MW: Inject RequestID & Slog Logger into context
    MW->>MW: Apply Rate Limiter & CORS Check
    MW->>MW: Authenticate JWT (if auth required)
    MW->>H: Forward to matched Handler
    H->>H: Decode & Validate JSON / URL Params
    H->>S: Call Domain Service(ctx, params)
    S->>R: Execute Query / Mutation(ctx, data)
    R->>DB: I/O Operation with Context Timeout
    DB-->>R: Result / Driver Error
    R-->>S: Return Entity / Wrapped Error (%w)
    S-->>H: Return Domain Model / Business Result
    H->>H: Map to HTTP Status & JSON Response Body
    H-->>MW: Response written (Status Code)
    MW->>MW: Push metric to In-Memory Traffic Buffer
    MW-->>User: HTTP Response (JSON / Static File)
```

---

## Folder Map & Package Responsibilities

```
backend-go/
├── cmd/
│   └── server/
│       └── main.go                  # Application entrypoint: wire DI, handle signals, listen
├── internal/
│   ├── config/                      # Strongly-typed environment configuration and validation
│   ├── server/                      # HTTP server initialization and chi router mounting
│   ├── middleware/                  # RequestID, logging, CORS, rate-limiting, auth, recovery
│   ├── handler/                     # HTTP transport controllers, JSON decoding, status codes
│   ├── service/                     # Business workflows, scoring algorithms, decision tree
│   ├── repository/                  # MongoDB collection queries, indexes, and aggregations
│   ├── domain/                      # Domain entities, constants, interfaces, and sentinel errors
│   └── platform/                    # Outbound HTTP clients (TMDB, OMDb, Brevo) and DB driver
├── testdata/
│   └── golden/                      # JSON contract test fixtures comparing Node vs Go
├── loadtest/                        # k6 and Vegeta load testing scenarios
├── docs/                            # Architecture, ADRs, migration logs, runbooks
├── Dockerfile                       # Non-root multi-stage container
├── docker-compose.yml               # Local dependencies (MongoDB container)
├── Makefile                         # Standard automation targets
└── .golangci.yml                    # Linter configuration
```

---

## Architectural Principles

1. **Inversion of Control**: Interfaces are declared at consumer boundaries (`internal/service` defines repository interfaces it needs; `internal/repository` implements them).
2. **Context Propagation**: Every database call, outbound HTTP request, and scraper invocation accepts `context.Context` with an enforced timeout.
3. **No Global State**: Zero global mutable variables. All components are instantiated via explicit constructors (`New...`) and wired in `main.go`.
4. **Resilience & Graceful Shutdown**: Listens for `SIGINT` / `SIGTERM`, allows in-flight requests 10 seconds to finish, flushes remaining traffic logs to MongoDB, and closes client connections cleanly.
