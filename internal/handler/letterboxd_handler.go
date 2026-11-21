package handler

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

//go:embed data/letterboxd_featured.json
var letterboxdFeaturedJSON []byte

var (
	letterboxdOnce        sync.Once
	letterboxdCollections []map[string]any
)

func loadLetterboxdCollections() []map[string]any {
	letterboxdOnce.Do(func() {
		if len(letterboxdFeaturedJSON) > 0 {
			_ = json.Unmarshal(letterboxdFeaturedJSON, &letterboxdCollections)
		}
	})
	return letterboxdCollections
}

// GetLetterboxdCollections returns curated Letterboxd lists formatted as Soulstash published collections
func (h *ContentHandler) GetLetterboxdCollections(w http.ResponseWriter, r *http.Request) {
	cols := loadLetterboxdCollections()
	if cols == nil {
		cols = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, cols)
}

// GetLetterboxdCollection returns a single Letterboxd collection by slug or name
func (h *ContentHandler) GetLetterboxdCollection(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		slug = strings.TrimSpace(r.URL.Query().Get("slug"))
	}
	col := FindLetterboxdCollection(slug)
	if col == nil {
		writeError(w, http.StatusNotFound, "Letterboxd collection not found")
		return
	}
	writeJSON(w, http.StatusOK, col)
}

// FindLetterboxdCollection finds a Letterboxd collection by slug or name (case-insensitive)
func FindLetterboxdCollection(identifier string) map[string]any {
	if identifier == "" {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(identifier))
	cols := loadLetterboxdCollections()
	for _, entry := range cols {
		slug, _ := entry["slug"].(string)
		if strings.EqualFold(slug, target) {
			return entry
		}
		if col, ok := entry["collection"].(map[string]any); ok {
			colSlug, _ := col["slug"].(string)
			if strings.EqualFold(colSlug, target) {
				return entry
			}
			colName, _ := col["name"].(string)
			if strings.EqualFold(colName, target) || strings.EqualFold(strings.ReplaceAll(colName, " ", "-"), target) {
				return entry
			}
		}
	}
	return nil
}
