# Configuration Reference

This document details every environment variable used by the backend service. All variables are derived exclusively from static analysis of `process.env.*` usages in the codebase.

| Variable Name | Purpose | Required / Optional | Default Value | Usage Scope |
|---|---|---|---|---|
| `PORT` | HTTP server port | Optional | `3000` | Server entrypoint listener |
| `NODE_ENV` / `APP_ENV` | Application environment (`production` vs `development`) | Optional | `development` | CORS policy, error detail, SPA proxying |
| `MONGODB_URI` | MongoDB connection URI string | **Required** | None | Database client connection |
| `MONGODB_DB_NAME` | MongoDB database name | **Required** | None | Database client selection |
| `JWT_SECRET` | Secret key for signing and verifying JWT tokens | **Required** in prod | `your-super-secret-jwt-key-change-this-in-production` | Auth token verification & generation |
| `BREVO_API_KEY` | Brevo (Sendinblue) transactional email API key | **Required** for email | None | OTP delivery for registration and password reset |
| `TMDB_BEARER_TOKEN` | TMDB API Read Access Token (Bearer) | **Required** for TMDB | None | Content catalog, discovery, credits, search |
| `TMDB_BASE_URL` | TMDB API Base URL | Optional | `https://api.tmdb.org` | TMDB API request routing |
| `OMDB_API_KEY` | Open Movie Database API key | Optional | None | IMDb ratings enrichment |
| `WATCHMODE_API_KEY` (or `WATCHMODE`) | Watchmode API key | Optional | None | Fallback streaming player source provider |
| `CF_CLEARANCE` | Cloudflare clearance cookie token | Optional | None | Playwright / HTTP scraping bypass |
| `VITE_DEV_SERVER_ORIGIN` | Vite development server host | Optional | `http://localhost:5173` | Development SPA proxy / live reload injection |
| `PLAYWRIGHT_BROWSERS_PATH` | Path where browser binaries are located | Optional | `default` | Headless browser execution |
| `RENDER` | Indicator flag that app is running on Render | Optional | None | Runtime environment adjustments |
