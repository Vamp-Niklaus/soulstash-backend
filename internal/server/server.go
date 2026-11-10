package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"soulstash/internal/config"
	"soulstash/internal/handler"
	"soulstash/internal/middleware"
	"soulstash/internal/platform/traffic"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// Server encapsulates the HTTP server, chi router, and dependencies
type Server struct {
	router        *chi.Mux
	httpServer    *http.Server
	cfg           *config.Config
	logger        *slog.Logger
	trafficBuffer *traffic.Buffer
	startTime     time.Time
}

// HandlerDeps holds the handler dependencies to mount
type HandlerDeps struct {
	AuthHandler       *handler.AuthHandler
	CollectionHandler *handler.CollectionHandler
	ContentHandler    *handler.ContentHandler
	AdminHandler      *handler.AdminHandler
	PagesHandler      *handler.PagesHandler
}

// New creates and configures the HTTP server and middleware pipelines
func New(cfg *config.Config, logger *slog.Logger, trafficBuf *traffic.Buffer, deps *HandlerDeps) *Server {
	r := chi.NewRouter()

	s := &Server{
		router:        r,
		cfg:           cfg,
		logger:        logger,
		trafficBuffer: trafficBuf,
		startTime:     time.Now(),
	}

	s.setupMiddleware()
	s.setupRoutes(deps)

	s.httpServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

func (s *Server) setupMiddleware() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.Logger(s.logger))
	s.router.Use(middleware.Recoverer(s.logger))
	s.router.Use(chimiddleware.Compress(5))
	s.router.Use(middleware.CORS(s.cfg.IsProduction))

	// In-memory traffic telemetry logging
	s.router.Use(middleware.TrafficLogging(s.trafficBuffer))

	// Cache control header policy matching Node.js
	s.router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if strings.HasPrefix(path, "/images/") || strings.HasPrefix(path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if strings.Contains(path, "/api/content") {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
				w.Header().Set("X-SPA-Content", "true")
			}
			next.ServeHTTP(w, r)
		})
	})
}

func (s *Server) setupRoutes(deps *HandlerDeps) {
	// Top-level Health, Readiness & Cron Keep-Alive endpoints (exempt from rate limits for Render & monitors)
	s.router.Get("/health", s.handleHealth)
	s.router.Get("/healthz", s.handleHealth)
	s.router.Get("/ping", s.handleHealth)
	s.router.Get("/cron/health", s.handleCronHealth)
	s.router.Get("/cron-health", s.handleCronHealth)
	s.router.Get("/keep-alive", s.handleCronHealth)

	rateLimiter := middleware.NewRateLimiter(1000, 15*time.Minute)

	// Base API route group with rate limiter
	s.router.Route("/api", func(r chi.Router) {
		r.Use(rateLimiter.Handler)

		// Health & readiness
		r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":    "OK",
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			})
		})

		r.Get("/ready", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "READY",
			})
		})

		// Mount Auth routes under /api/auth
		if deps != nil && deps.AuthHandler != nil {
			r.Route("/auth", func(authR chi.Router) {
				deps.AuthHandler.RegisterRoutes(authR)
			})
		}

		// Mount Unified /api/user routes (Auth + Collections) with zero shadowing
		r.Route("/user", func(userR chi.Router) {
			if deps != nil && deps.AuthHandler != nil {
				deps.AuthHandler.RegisterRoutes(userR)
			}
			if deps != nil && deps.CollectionHandler != nil {
				deps.CollectionHandler.RegisterUserRoutes(userR)
			}
		})

		// Mount Content and Collection routes
		if deps != nil {
			if deps.CollectionHandler != nil {
				deps.CollectionHandler.RegisterRoutes(r)
			}
			if deps.ContentHandler != nil {
				deps.ContentHandler.RegisterRoutes(r)
			}
		}
	})

	// Mount Admin routes under /api/admin
	if deps != nil && deps.AdminHandler != nil {
		s.router.Route("/api/admin", func(r chi.Router) {
			deps.AdminHandler.RegisterRoutes(r)
		})
	}

	s.router.Get("/api/kartoons/resolve-stream", s.handleKartoonsResolveStream)

	// Image proxy endpoint matching Node server.js:203
	s.router.Get("/image/{path}", func(w http.ResponseWriter, req *http.Request) {
		imagePath := chi.URLParam(req, "path")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"imageUrl": fmt.Sprintf("https://image.tmdb.org/t/p/w500/%s", imagePath),
		})
	})

	// Static assets
	s.setupStaticFiles()

	// Mount SPA page routes
	if deps != nil && deps.PagesHandler != nil {
		deps.PagesHandler.RegisterRoutes(s.router)
	}

	// 404 handler matching Node server.js:210
	s.router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Route not found",
		})
	})
}

func (s *Server) setupStaticFiles() {
	workDir, _ := os.Getwd()
	dirs := []string{
		workDir,
		filepath.Dir(workDir),
		"C:\\Users\\rakes\\Documents\\Soulstash",
	}

	var assetsDir string
	for _, d := range dirs {
		candidate := filepath.Join(d, "assets", "images")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			assetsDir = candidate
			break
		}
	}

	if assetsDir != "" {
		fileServer := http.FileServer(http.Dir(assetsDir))
		s.router.Handle("/images/*", http.StripPrefix("/images", fileServer))
	}
}

func (s *Server) Router() *chi.Mux {
	return s.router
}

func (s *Server) Start() error {
	s.logger.Info("Server running on port", slog.Int("port", s.cfg.Port))
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down HTTP server gracefully...")
	if s.trafficBuffer != nil {
		s.trafficBuffer.Close()
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "OK",
		"service":   "soulstash-backend",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) handleCronHealth(w http.ResponseWriter, req *http.Request) {
	uptime := time.Since(s.startTime)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := float64(m.Alloc) / 1024 / 1024
	goroutines := runtime.NumGoroutine()

	// Check if client expects HTML (browser visit)
	accept := req.Header.Get("Accept")
	if strings.Contains(accept, "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Soulstash Cron & Health Status</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #f8fafc; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; }
    .card { background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 2rem; max-width: 480px; width: 90%%; box-shadow: 0 10px 25px rgba(0,0,0,0.5); }
    .badge { display: inline-flex; align-items: center; gap: 8px; background: #064e3b; color: #34d399; padding: 6px 14px; border-radius: 9999px; font-weight: 600; font-size: 0.875rem; margin-bottom: 1rem; }
    .pulse { width: 8px; height: 8px; background: #34d399; border-radius: 50%%; box-shadow: 0 0 10px #34d399; }
    h1 { font-size: 1.5rem; margin: 0 0 0.5rem 0; color: #ffffff; }
    p.sub { color: #94a3b8; font-size: 0.875rem; margin: 0 0 1.5rem 0; }
    .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 1.5rem; }
    .stat { background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 12px; }
    .stat-label { font-size: 0.75rem; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; }
    .stat-val { font-size: 1.125rem; font-weight: 600; color: #f1f5f9; margin-top: 4px; }
    .footer { font-size: 0.75rem; color: #64748b; text-align: center; border-top: 1px solid #334155; padding-top: 1rem; }
  </style>
</head>
<body>
  <div class="card">
    <div class="badge"><span class="pulse"></span> LIVE & HEALTHY</div>
    <h1>Soulstash Backend</h1>
    <p class="sub">Cron Keep-Alive & Health Check Status</p>
    <div class="grid">
      <div class="stat"><div class="stat-label">Uptime</div><div class="stat-val">%s</div></div>
      <div class="stat"><div class="stat-label">Memory Alloc</div><div class="stat-val">%.2f MB</div></div>
      <div class="stat"><div class="stat-label">Goroutines</div><div class="stat-val">%d</div></div>
      <div class="stat"><div class="stat-label">Environment</div><div class="stat-val">%s</div></div>
    </div>
    <div class="footer">Last ping: %s UTC &bull; Render Keep-Alive Active</div>
  </div>
</body>
</html>`, formatDuration(uptime), allocMB, goroutines, s.cfg.AppEnv, time.Now().UTC().Format("15:04:05"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            "healthy",
		"service":           "soulstash-backend",
		"uptime":            formatDuration(uptime),
		"uptime_seconds":    int64(uptime.Seconds()),
		"timestamp":         time.Now().UTC().Format(time.RFC3339),
		"environment":       s.cfg.AppEnv,
		"memory_alloc_mb":   allocMB,
		"goroutines":        goroutines,
		"render_keep_alive": true,
	})
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
