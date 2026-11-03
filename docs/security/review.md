# Security Architecture Review & Audit

## 1. Authentication & Authorization Controls

### Password Hashing
- **Standard**: Uses `golang.org/x/crypto/bcrypt` with standard adaptive work factor (cost 10).
- **Parity with Node**: Node used `bcryptjs`. Go bcrypt is fully compatible with existing hashes stored in MongoDB.
- **Fail-Safe**: Constant-time comparison in `bcrypt.CompareHashAndPassword` defends against timing attacks.

### Session & JWT Token Verification
- Signed using HMAC-SHA256 (`jwt.SigningMethodHS256`) with a cryptographically enforced secret (`JWT_SECRET`).
- **Enforcement**: In production environments (`NODE_ENV=production`), the application halts startup if `JWT_SECRET` is shorter than 32 characters or matches insecure development defaults (`internal/config/config.go`).
- **Authorization Scopes**:
  - Unauthenticated routes: `/api/auth/register`, `/api/auth/login`, `/api/auth/check-username`, `/api/health`, `/api/ready`.
  - Authenticated routes: `/api/auth/me`, `/api/user/collections`, `/api/user/favorites`.
  - Admin-only routes: Protected by chained `AuthenticateToken` + `ensureAdmin` middleware (`internal/handler/admin_handler.go`), returning `401 Unauthorized` for missing/invalid tokens and `403 Forbidden` for non-admin accounts.

---

## 2. Injection & Query Hardening

### NoSQL Injection Prevention
- **Elimination of `$where` and dynamic string queries**: All MongoDB database interactions use typed BSON documents (`bson.M`, `bson.D`) via the official `go.mongodb.org/mongo-driver/v2`.
- Direct user inputs are validated and cast into explicit Go types (`string`, `int`, `bson.ObjectID`) before query parameterization.

### Strict Input Sanitization
- Input text fields (e.g. search queries) undergo whitespace stripping and non-alphanumeric character normalization (`internal/service/content_scoring.go`).
- Path and query parameters are parsed via `strconv.Atoi` or regex validation before passing down to internal services.

---

## 3. SSRF & Network Protections

### Outbound Fetching & Image Proxy
- The image proxy handler (`/image/*`) restricts upstream fetches strictly to configured image CDN hosts (e.g. `image.tmdb.org`).
- **Private IP & Metadata Blocking**: Outbound scraper and proxy HTTP clients block requests targeting internal loopback or link-local metadata ranges (`127.0.0.1`, `169.254.169.254`, `10.0.0.0/8`, `192.168.0.0/16`, `172.16.0.0/12`).

---

## 4. Rate Limiting & Denial of Service (DoS)

### Rate Limiting Middleware (`internal/middleware/rate_limit.go`)
- In-memory token bucket / sliding window rate limiter prevents brute-force login attempts and scraping floods.
- Default threshold: 100 requests per minute per IP address. Exceeded requests receive `HTTP 429 Too Many Requests` with a descriptive payload.

### Request Body Limit & Resource Exhaustion Protection
- Maximum incoming payload size is constrained using `http.MaxBytesReader` to prevent memory exhaustion via oversized POST/PUT requests.
- All outbound HTTP requests enforce context deadlines (10s for OMDb, 15s for TMDB).

---

## 5. Structured Logging & Zero-Secrets Guarantee

### Logging Compliance
- Configured with `log/slog` structured JSON output.
- **Sensitive Fields Redaction**: Passwords, raw JWT tokens, and OTP reset tokens are never written to logs.
- Sensitive environment variables (`JWT_SECRET`, `MONGO_URI`, `BREVO_API_KEY`, `OMDB_API_KEY`) are read once at startup into strongly typed config structs and excluded from debug dumps.

---

## 6. Container & Runtime Hardening

### Minimal Non-Root Image (`Dockerfile`)
- **Multi-Stage Build**: Compilation occurs in `golang:1.24-alpine` build stage; production runtime contains only the compiled static binary and trusted CA certificates.
- **Non-Root User**: Application executes under unprivileged UID `10001` (`appuser:appgroup`).
- **Read-Only Filesystem Compatible**: Requires no write access to container root filesystem.
