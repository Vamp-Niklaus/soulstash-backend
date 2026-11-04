# Known Quirks & Node.js Behavioral Specifics

This document records idiosyncrasies, legacy schema fixes, edge-case sentinels, and quirks discovered in the Node.js implementation that must be preserved in the Go migration for exact behavioral parity.

---

## 1. IMDb Rating Sentinel Value `10.0`
- **Location**: `util/imdbRatings.js` line 7, 22
- **Behavior**:
  When an IMDb rating cannot be found or is invalid, the Node.js code writes the sentinel float `10.0` (`INVALID_IMDB_SENTINEL = 10.0`) into MongoDB's `Ratings` collection.
  However, during reads, `validImdbRating(value)` checks `if (parsed === 10.0) return null;`.
  Hence, `10.0` is treated as a sentinel for "lookup attempted but rating unavailable", preventing eternal re-fetching while returning `null` to the client.
- **Go Migration Rule**:
  Replicate this exact behavior. Do not substitute `0.0` or `-1.0` in the database without a database migration. When parsing for API output, filter `10.0` to `null`.

---

## 2. Dual Mounting of Auth Routes
- **Location**: `server.js` lines 194–195
- **Behavior**:
  ```js
  app.use('/api/auth', require('./routes/auth'));
  app.use('/api/user', require('./routes/auth'));
  ```
  All endpoints defined in `routes/auth.js` can be accessed via either prefix:
  - `/api/auth/register` and `/api/user/register`
  - `/api/auth/login` and `/api/user/login`
  - `/api/auth/me` and `/api/user/me`
  - `/api/auth/profile` and `/api/user/profile`
  - `/api/auth/verify-token` and `/api/user/verify-token`
- **Go Migration Rule**:
  Register all auth handler routes on both `/api/auth/*` and `/api/user/*`.

---

## 3. Plural & Singular Alias Endpoints
- **Location**: `routes/content.js` lines 2228, 2488, 2510
- **Behavior**:
  Express route definitions bind multiple paths to the same handler:
  - `/api/movies/:id` and `/api/movie/:id`
  - `/api/person/:id` and `/api/people/:id`
  - `/api/person/:id/credits` and `/api/people/:id/credits`
- **Go Migration Rule**:
  Expose both path variants in the Go router.

---

## 4. Dropped Obsolete Index on `PlayerSources`
- **Location**: `db.js` lines 58–64
- **Behavior**:
  On startup, the code explicitly drops index `player_sources_search_key_unique` because TV series episodes have null or duplicate search keys for common series titles.
- **Go Migration Rule**:
  Ensure the Go initialization code does NOT create a unique index on `searchKey`, and gracefully drops the legacy index if it exists.

---

## 5. Dual OTP Storage Mechanism and OTP Privacy
- **Location**: `routes/auth.js` lines 9, 300
- **Behavior**:
  - Registration OTPs (`send-otp` -> `verify-otp-and-register`) are stored in an in-memory `Map` (`otpStore`) with a 10-minute expiration.
  - Password reset OTPs (`forgot-password` -> `reset-password`) are stored in MongoDB's `password_resets` collection with a 15-minute expiration.
- **Go Migration Rule**:
  Maintain an in-memory thread-safe store for registration OTPs and MongoDB collection for password resets, with the same validation and timeouts. Never include OTP values in API responses or logs in any environment. Store a registration OTP only after its email sends successfully; if delivery fails, discard it and return an error. If reset email delivery fails, remove the pending reset record and return an error. The API/UI must show only a generic delivery message.

---

## 6. Monotonic `collectionVersion` & Header Synchronization
- **Location**: `routes/collections.js` lines 44–52, 94–99
- **Behavior**:
  User library updates increment `user.collectionVersion`.
  Endpoints read `X-Collection-Version` from request headers. If provided and different from the current version, the response sets header `X-Collection-Stale: true` and payload `staleResolved: true`.
- **Go Migration Rule**:
  Implement this header parsing and response logic identically in the Go collection service.

---

## 7. TMDB Hostname Substitution
- **Location**: `util/imdbRatings.js` line 5
- **Behavior**:
  `TMDB_BASE_URL` replaces `api.themoviedb.org` with `api.tmdb.org` to comply with internal DNS/proxy rules.
- **Go Migration Rule**:
  Normalize TMDB base URL to `api.tmdb.org`.
