package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

type PagesHandler struct {
	spaDistPath  string
	spaSrcPath   string
	isProduction bool
}

func NewPagesHandler(isProduction bool) *PagesHandler {
	candidates := []string{
		"C:\\Users\\rakes\\Documents\\Soulstash",
		"..",
		".",
	}

	distPath := ""
	srcPath := ""

	for _, c := range candidates {
		d := filepath.Join(c, "spa", "dist", "index.html")
		if _, err := os.Stat(d); err == nil && distPath == "" {
			distPath = d
		}
		s := filepath.Join(c, "spa", "index.html")
		if _, err := os.Stat(s); err == nil && srcPath == "" {
			srcPath = s
		}
	}

	return &PagesHandler{
		spaDistPath:  distPath,
		spaSrcPath:   srcPath,
		isProduction: isProduction,
	}
}

func (h *PagesHandler) RegisterRoutes(r chi.Router) {
	pages := []string{
		"/",
		"/explore",
		"/login",
		"/register",
		"/collections",
		"/edit",
		"/terms-of-service",
		"/privacy-policy",
		"/admin",
		"/movie/{id}",
		"/series/{id}",
		"/person/{id}",
		"/collection/{collectionName}",
		"/user/{username}",
		"/user/{username}/collections",
		"/user/{username}/collections/{collectionName}",
		"/user/{username}/collection",
		"/user/{username}/collection/{collectionName}",
		"/genre/{id}",
		"/genre/{id}/{name}",
	}

	for _, p := range pages {
		r.Get(p, h.ServeSPA)
	}

	// Catch-all SPA handler for unmapped GET paths (excluding API/image/assets)
	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		if strings.HasPrefix(path, "/api") ||
			strings.HasPrefix(path, "/image") ||
			strings.HasPrefix(path, "/assets") ||
			strings.HasPrefix(path, "/js") ||
			strings.HasPrefix(path, "/images") ||
			strings.HasPrefix(path, "/favicon") {
			writeError(w, http.StatusNotFound, "Route not found")
			return
		}
		h.ServeSPA(w, req)
	})
}

func (h *PagesHandler) ServeSPA(w http.ResponseWriter, r *http.Request) {
	// 1. Try serving spa/dist/index.html
	if h.spaDistPath != "" {
		if content, err := os.ReadFile(h.spaDistPath); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
	}

	// 2. Try serving spa/index.html
	if h.spaSrcPath != "" {
		if content, err := os.ReadFile(h.spaSrcPath); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
	}

	// 3. Fallback lightweight HTML shell
	fallbackHTML := `<!DOCTYPE html><html><head><title>Soulstash</title></head><body><div id="root">Soulstash</div></body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, fallbackHTML)
}
