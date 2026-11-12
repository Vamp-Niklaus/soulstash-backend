package handler

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// SearchTMDBCollections searches TMDB movie collections (franchises) by name.
// The navbar "Collections" tab merges these with user-published collections and
// labels their publisher as "TMDB", along with the item_count of movies in the franchise.
func (h *ContentHandler) SearchTMDBCollections(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}})
		return
	}

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

	raw, _ := data["results"].([]any)
	if len(raw) > 15 {
		raw = raw[:15]
	}

	results := make([]map[string]any, len(raw))
	var wg sync.WaitGroup
	for i, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok || getFloat(item["id"]) == 0 {
			continue
		}
		id := int(getFloat(item["id"]))
		results[i] = map[string]any{
			"id":            id,
			"name":          item["name"],
			"overview":      item["overview"],
			"poster_path":   item["poster_path"],
			"backdrop_path": item["backdrop_path"],
			"item_count":    0,
		}
		wg.Add(1)
		go func(idx int, colID int) {
			defer wg.Done()
			detail, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/collection/%d", colID), map[string]string{"language": "en-US"})
			if err == nil && detail != nil {
				if parts, ok := detail["parts"].([]any); ok {
					results[idx]["item_count"] = len(parts)
				}
			}
		}(i, id)
	}
	wg.Wait()

	validResults := make([]map[string]any, 0, len(results))
	for _, res := range results {
		if res != nil && res["id"] != nil {
			validResults = append(validResults, res)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": validResults})
}
