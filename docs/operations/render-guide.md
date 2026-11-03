# Render Deployment & Keep-Alive Guide

This guide provides end-to-end instructions for deploying the **Soulstash Go Backend** on [Render](https://render.com) as a Web Service, configuring health checks, and setting up automated cron keep-alive pings to prevent free-tier instance sleep.

---

## 1. Quick Settings for Render Web Service

When creating a new **Web Service** on Render connected to `https://github.com/Vamp-Niklaus/soulstash-backend.git`:

| Setting | Value |
|---|---|
| **Name** | `soulstash-backend` |
| **Region** | Oregon (US West) or Singapore / Frankfurt (choose closest to your MongoDB) |
| **Branch** | `main` |
| **Root Directory** | *(leave blank - repository root)* |
| **Runtime** | `Go` |
| **Build Command** | `go build -o server ./cmd/server` |
| **Start Command** | `./server` |
| **Plan** | Free (or Starter / Standard) |

---

## 2. Health Check Configuration on Render

Render periodically checks your application to ensure it is healthy before routing live traffic:

1. In your Render Dashboard, go to **Settings** $\rightarrow$ **Health Check Path**.
2. Set the path to:
   ```text
   /health
   ```
   *(Or `/cron/health` if you want full metrics).*
3. Render will now verify that `GET /health` returns `HTTP 200 OK`.

---

## 3. Environment Variables on Render

In the **Environment** tab of your Render service, add the following variables:

| Key | Example Value | Description |
|---|---|---|
| `PORT` | `10000` | Render injects this automatically; Go server reads it dynamically |
| `NODE_ENV` | `production` | Enables production security checks |
| `MONGO_URI` *(or `MONGODB_URI`)* | `mongodb+srv://user:pass@cluster.mongodb.net/?retryWrites=true&w=majority` | Your MongoDB connection string |
| `DB_NAME` *(or `MONGODB_DB_NAME`)* | `soulstash` | Database name |
| `JWT_SECRET` | *(Random 32+ character string)* | Secret for signing user JWT tokens |
| `TMDB_BEARER_TOKEN` | `eyJhbGci...` | TMDB API bearer token |
| `TMDB_BASE_URL` | `https://api.tmdb.org` | TMDB API base URL |
| `OMDB_API_KEY` | *(your key)* | IMDb ratings enrichment |
| `BREVO_API_KEY` | *(your key)* | Transactional email OTP delivery |
| `CORS_ORIGIN` | `*` *(or your frontend URL)* | Allowed CORS origin |

---

## 4. Cron Job & Keep-Alive Setup (Prevent Instance Sleep)

Render's free tier spins down instances after 15 minutes of inactivity. To keep your backend warm and instantly responsive:

### Available Keep-Alive & Health Endpoints
- `GET /health` or `GET /healthz` $\rightarrow$ Fast JSON `200 OK`.
- `GET /cron/health` or `GET /cron-health` or `GET /keep-alive`:
  - **Browser (HTML)**: Shows a dark-mode status page with live uptime, memory allocation, goroutines count, and environment status.
  - **HTTP/Bot (JSON)**: Returns `{ "status": "healthy", "uptime": "...", "render_keep_alive": true }`.

### Option A: Render Cron Job (Inside Render)
1. In Render Dashboard, click **New +** $\rightarrow$ **Cron Job**.
2. Connect to the same repository.
3. Schedule:
   ```text
   */10 * * * *
   ```
   *(Runs every 10 minutes).*
4. Command:
   ```bash
   curl -s -f https://<your-render-subdomain>.onrender.com/cron/health
   ```

### Option B: Free External Monitoring (UptimeRobot / Cron-Job.org)
1. Go to [cron-job.org](https://cron-job.org) or [UptimeRobot.com](https://uptimerobot.com).
2. Create a new monitor / cron job:
   - **URL**: `https://<your-app-name>.onrender.com/cron/health`
   - **Interval**: Every 5 or 10 minutes
   - **HTTP Method**: `GET`
3. This sends an active keep-alive ping, preventing your Render instance from spinning down and guaranteeing sub-10ms response times.
