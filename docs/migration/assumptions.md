# Migration Assumptions

This document lists assumptions made during the Node.js -> Go migration process. Any decisions made in absence of explicit instructions are recorded here.

---

## 1. Cloud & Container Deployment Target
- **Context**: The deployment configuration files (`render.yaml`, `.render.yml`) are excluded from scope.
- **Assumption**: We assume a portable, cloud-agnostic Docker container deployment. The Dockerfile uses a multi-stage build producing a non-root, minimal distroless/scratch or alpine image that can run on Render, AWS ECS, GCP Cloud Run, Fly.io, or Kubernetes.
- **Impact**: Zero platform lock-in; standard environment variable injection.

---

## 2. Secrets & Environment Handling
- **Context**: `.env*` files and secret values are strictly out of scope.
- **Assumption**: All environment keys have been derived from code AST / grep analysis. Test suites rely entirely on mock implementations or local containerized instances, never touching external live credentials or production databases.
- **Impact**: Maximum security and repeatability.

---

## 3. Web Scraping & Playwright Parity
- **Context**: Node uses Playwright for scraping Multimovies and IMDb filmography.
- **Assumption**:
  In Go, all standard HTML scraping (such as IMDb filmography and Multimovies direct server endpoints) is implemented with high-performance Go HTTP clients and `golang.org/x/net/html` / `github.com/PuerkitoBio/goquery`. For Cloudflare-protected endpoints that require JavaScript execution, the Go architecture provides an abstracted scraper interface supporting either direct HTTP stealth fetching or an external/internal headless runner.
- **Impact**: Dramatic memory reduction (from >300MB Node Playwright footprint down to <20MB Go process) while maintaining full URL and DOM extraction parity.

---

## 4. SPA Static Asset Serving
- **Context**: The `spa/` folder is frontend code and out of scope for migration, but the backend server must serve built assets and index fallback.
- **Assumption**: In Go, the server checks if `../spa/dist` or `./public` exists. If present, it serves static files with long-lived immutable cache headers for `/assets/*` and `/images/*`, and falls back to `index.html` for client-side routing. In development mode with `NODE_ENV != production`, it proxies or serves HTML with Vite live reload scripts if Vite is detected.
- **Impact**: Full backward compatibility with the existing single-page application.
