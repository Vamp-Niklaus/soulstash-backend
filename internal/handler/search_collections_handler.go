package handler

import (
	"net/http"
	"strings"
)

// SearchTMDBCollections searches TMDB movie collections (franchises) by name.
// The navbar "Collections" tab merges these with user-published collections and
// labels their publisher as "TMDB".
func (h *ContentHandler) SearchTMDBCollections(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}})
		return
	}

	adminMode := h.searchAdminMode(r)
	data, err := h.tmdbClient.Get(r.Context(), "/3/search/collection", map[string]string{
		"query":         query,
		"include_adult": "false",
		"language":      "en-US",
		"page":          "1",
	})
	if err != nil || data == nil {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}})
		return
	}

	h.filterContentResults(data, adminMode)
	raw, _ := data["results"].([]any)
	results := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok || getFloat(item["id"]) == 0 {
			continue
		}
		results = append(results, map[string]any{
			"id":            item["id"],
			"name":          item["name"],
			"overview":      item["overview"],
			"poster_path":   item["poster_path"],
			"backdrop_path": item["backdrop_path"],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": results})
}
