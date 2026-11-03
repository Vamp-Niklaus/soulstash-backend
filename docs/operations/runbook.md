# Soulstash Backend Operations Runbook

## 1. Quick Reference & Service Operations

### Running Locally (Native Go)
```bash
# Load environment and start server
cd backend-go
go run ./cmd/server
```

### Running with Docker Compose
```bash
# Start local MongoDB and backend container
docker-compose up -d

# View real-time logs
docker-compose logs -f app

# Stop containers
docker-compose down
```

### Building the Production Binary
```bash
# Compile optimized static binary
make build
# Binary is generated at bin/server (or bin/server.exe on Windows)
```

### Building the Production Docker Image
```bash
make docker
# Produces minimal non-root image soulstash-backend:latest
```

---

## 2. Health & Readiness Probes

The Go server exposes standard Kubernetes/load-balancer health endpoints:

### Liveness Probe (`GET /api/health`)
- Returns `200 OK` with JSON `{ "status": "ok", "service": "soulstash-backend" }` if HTTP listener and event loop are responsive.
- Used by orchestrators to restart frozen containers.

### Readiness Probe (`GET /api/ready`)
- Returns `200 OK` with `{ "status": "ready", "database": "connected" }` when active MongoDB ping succeeds.
- Returns `503 Service Unavailable` with `{ "status": "unhealthy", "database": "error message" }` if database ping fails.
- Used by load balancers to route traffic away from degrading instances.

---

## 3. Configuration Management

Configuration is loaded from environment variables and validated at startup (`internal/config/config.go`).

| Variable | Type | Default | Description |
|---|---|---|---|
| `PORT` | Int | `5000` | HTTP port to bind |
| `NODE_ENV` | String | `development` | Environment mode (`development` or `production`) |
| `MONGO_URI` | String | `mongodb://localhost:27017` | MongoDB connection URI |
| `DB_NAME` | String | `soulstash` | Database name |
| `JWT_SECRET` | String | Required | Secret for signing auth tokens (>=32 chars in prod) |
| `TMDB_API_KEY` | String | Optional | Bearer token / API key for TMDB catalog API |
| `OMDB_API_KEY` | String | Optional | API key for IMDb ratings enrichment |
| `BREVO_API_KEY` | String | Optional | API key for sending email verification OTPs |
| `CORS_ORIGIN` | String | `*` | Allowed CORS origin (comma-separated or `*`) |
| `PUBLIC_DIR` | String | `../spa/dist` | Path to frontend SPA static build directory |

---

## 4. Operational Maintenance Procedures

### MongoDB Index Initialization
Indexes are automatically created at application startup by `internal/platform/mongodb/client.go`:
- `users`: Unique index on `username`
- `Ratings`: Compound index on `{ tmdbId: 1, mediaType: 1 }`
- `password_resets`: TTL index on `expiresAt` (automatically purged by MongoDB after 15m)

To manually re-index via Mongo shell:
```javascript
use soulstash;
db.users.createIndex({ username: 1 }, { unique: true });
db.Ratings.createIndex({ tmdbId: 1, mediaType: 1 }, { unique: true });
db.password_resets.createIndex({ expiresAt: 1 }, { expireAfterSeconds: 0 });
```

### Telemetry Flushing & Traffic Logs
Telemetry entries are buffered in memory and batched to MongoDB every 5 seconds.
- **Graceful Shutdown**: Sending `SIGINT` or `SIGTERM` triggers `internal/platform/traffic/buffer.go:Drain()`, flushing all in-flight telemetry logs to MongoDB before process termination.
- **Querying Traffic Stats**:
  ```bash
  curl -H "Authorization: Bearer <ADMIN_TOKEN>" http://localhost:5000/api/admin/trafficLogs/stats
  ```

---

## 5. Incident Response & Troubleshooting

### Scenario A: Readiness Probe Fails (503 Service Unavailable)
1. Check MongoDB server status:
   ```bash
   mongosh "<MONGO_URI>" --eval "db.adminCommand('ping')"
   ```
2. Verify network routing and firewall rules between backend host and MongoDB cluster.
3. Check backend logs for connection timeout details:
   ```bash
   docker logs --tail 100 soulstash-backend | grep "mongo"
   ```

### Scenario B: TMDB Upstream 504 Gateway Timeout
- Go HTTP client implements strict 15-second per-request context timeouts.
- Content handlers fallback gracefully: if TMDB fails, detailed error response is logged and client receives clean `404 Not Found` or cached database entry instead of hanging connections.

### Scenario C: High Memory / Goroutine Spike
1. Inspect running goroutines:
   ```bash
   # If pprof is enabled or by checking process metrics
   ps aux | grep server
   ```
2. The Go backend typically consumes ~18MB RSS idle and ~45MB under heavy load. If memory exceeds 150MB, investigate client connection pooling or unclosed response bodies.
