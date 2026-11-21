package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TMDB has no "list collections" endpoint. It only offers /search/collection (by name) and
// /collection/{id}. So collections come from two places:
//   - SearchTMDBCollections: name search used by the navbar "Collections" tab.
//   - GetFeaturedTMDBCollections: a curated list of well known franchises (resolved by name) for /posted-collections.
// Both hide empty collections and collections that contain adult titles, following the admin mode
// (0 = public / adult hidden, 1 = everything, 2 = adult only).

type tmdbCollectionCacheEntry struct {
	data map[string]any
	at   time.Time
}

type tmdbFeaturedCacheEntry struct {
	entries []map[string]any
	at      time.Time
}

const (
	tmdbCollectionDetailTTL = 6 * time.Hour
	tmdbFeaturedTTL         = 6 * time.Hour
)

var (
	tmdbCollectionDetailCache sync.Map // int -> tmdbCollectionCacheEntry
	tmdbFeaturedCache         sync.Map // int (adminMode) -> tmdbFeaturedCacheEntry
)

// Franchises shown on /posted-collections. Each is looked up by name in TMDB; names that
// are not found are skipped, so a wrong name never breaks the page.
var tmdbFeaturedCollectionNames = []string{
	"Star Wars Collection",
	"The Lord of the Rings Collection",
	"Harry Potter Collection",
	"The Dark Knight Collection",
	"The Matrix Collection",
	"Mission: Impossible Collection",
	"James Bond Collection",
	"Fast & Furious Collection",
	"Jurassic Park Collection",
	"John Wick Collection",
	"Toy Story Collection",
	"The Godfather Collection",
	"Pirates of the Caribbean Collection",
	"The Hunger Games Collection",
	"Back to the Future Collection",
	"Indiana Jones Collection",
	"Alien Collection",
	"Terminator Collection",
	"Despicable Me Collection",
	"Shrek Collection",
	"Kung Fu Panda Collection",
	"Ice Age Collection",
	"How to Train Your Dragon Collection",
	"The Conjuring Collection",
	"Iron Man Collection",
	"Guardians of the Galaxy Collection",
	"Avengers Collection",
	"Rocky Collection",
}

func (h *ContentHandler) tmdbCollectionDetail(ctx context.Context, id int) map[string]any {
	if v, ok := tmdbCollectionDetailCache.Load(id); ok {
		if entry := v.(tmdbCollectionCacheEntry); time.Since(entry.at) < tmdbCollectionDetailTTL {
			return entry.data
		}
	}
	data, err := h.tmdbClient.Get(ctx, fmt.Sprintf("/3/collection/%d", id), map[string]string{"language": "en-US"})
	if err != nil || data == nil {
		return nil
	}
	tmdbCollectionDetailCache.Store(id, tmdbCollectionCacheEntry{data: data, at: time.Now()})
	return data
}

// tmdbCollectionVisible reports whether a collection may be listed for the given admin mode.
// Empty collections are never listed. Public mode (0) hides the whole collection when any
// part is adult; mode 1 shows everything; mode 2 (adult only) needs at least one adult part.
func (h *ContentHandler) tmdbCollectionVisible(detail map[string]any, adminMode int) bool {
	parts, _ := detail["parts"].([]any)
	if len(parts) == 0 {
		return false
	}
	if adminMode == 1 {
		return true
	}
	anyVisible, allVisible := false, true
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if searchItemVisible(part, adminMode, h.adultEngine) {
			anyVisible = true
		} else {
			allVisible = false
		}
	}
	if adminMode == 2 {
		return anyVisible
	}
	return allVisible
}

func tmdbCollectionParts(detail map[string]any) []map[string]any {
	raw, _ := detail["parts"].([]any)
	parts := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if part, ok := entry.(map[string]any); ok && getFloat(part["id"]) > 0 {
			parts = append(parts, part)
		}
	}
	return parts
}

// tmdbCollectionPoster returns the collection poster, or the first part's poster when it has none.
func tmdbCollectionPoster(detail map[string]any) any {
	if p, _ := detail["poster_path"].(string); p != "" {
		return p
	}
	for _, part := range tmdbCollectionParts(detail) {
		if p, _ := part["poster_path"].(string); p != "" {
			return p
		}
	}
	return nil
}

// SearchTMDBCollections searches TMDB collections (franchises) by name for the navbar
// "Collections" tab. Publisher is labelled "TMDB" by the SPA.
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

	raw, _ := data["results"].([]any)
	if len(raw) > 12 {
		raw = raw[:12]
	}

	results := make([]map[string]any, len(raw))
	var wg sync.WaitGroup
	for i, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok || getFloat(item["id"]) == 0 {
			continue
		}
		wg.Add(1)
		go func(idx int, item map[string]any) {
			defer wg.Done()
			id := int(getFloat(item["id"]))
			detail := h.tmdbCollectionDetail(r.Context(), id)
			// Not listed: collection with no movies, or one the current admin mode may not see.
			if detail == nil || !h.tmdbCollectionVisible(detail, adminMode) {
				return
			}
			name, _ := detail["name"].(string)
			if name == "" {
				name, _ = item["name"].(string)
			}
			overview, _ := detail["overview"].(string)
			if overview == "" {
				overview, _ = item["overview"].(string)
			}
			results[idx] = map[string]any{
				"id":            id,
				"name":          name,
				"overview":      overview,
				"poster_path":   tmdbCollectionPoster(detail),
				"backdrop_path": detail["backdrop_path"],
				"item_count":    len(tmdbCollectionParts(detail)),
			}
		}(i, item)
	}
	wg.Wait()

	valid := make([]map[string]any, 0, len(results))
	for _, res := range results {
		if res != nil {
			valid = append(valid, res)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": valid})
}

// GetFeaturedTMDBCollections returns curated TMDB franchises in the same shape as
// /api/collections/published entries ({username: "tmdb", collection: {...}}), so the
// Lists page can show them next to user collections.
func (h *ContentHandler) GetFeaturedTMDBCollections(w http.ResponseWriter, r *http.Request) {
	adminMode := h.searchAdminMode(r)
	if v, ok := tmdbFeaturedCache.Load(adminMode); ok {
		if entry := v.(tmdbFeaturedCacheEntry); time.Since(entry.at) < tmdbFeaturedTTL {
			writeJSON(w, http.StatusOK, entry.entries)
			return
		}
	}

	slots := make([]map[string]any, len(tmdbFeaturedCollectionNames))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, name := range tmdbFeaturedCollectionNames {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			found, err := h.tmdbClient.Get(r.Context(), "/3/search/collection", map[string]string{
				"query":         name,
				"include_adult": "false",
				"language":      "en-US",
				"page":          "1",
			})
			if err != nil || found == nil {
				return
			}
			list, _ := found["results"].([]any)
			var pick map[string]any
			for _, entry := range list {
				item, ok := entry.(map[string]any)
				if !ok || getFloat(item["id"]) == 0 {
					continue
				}
				if pick == nil {
					pick = item
				}
				if itemName, _ := item["name"].(string); strings.EqualFold(itemName, name) {
					pick = item
					break
				}
			}
			if pick == nil {
				return
			}
			id := int(getFloat(pick["id"]))
			detail := h.tmdbCollectionDetail(r.Context(), id)
			if detail == nil || !h.tmdbCollectionVisible(detail, adminMode) {
				return
			}
			parts := tmdbCollectionParts(detail)
			movies := make([]any, 0, len(parts))
			for _, part := range parts {
				movies = append(movies, map[string]any{
					"id":            part["id"],
					"title":         part["title"],
					"media_type":    "movie",
					"poster_path":   part["poster_path"],
					"backdrop_path": part["backdrop_path"],
					"release_date":  part["release_date"],
				})
			}
			colName, _ := detail["name"].(string)
			overview, _ := detail["overview"].(string)
			banner := ""
			if b, _ := detail["backdrop_path"].(string); b != "" {
				banner = "https://image.tmdb.org/t/p/w1280" + b
			}
			slots[idx] = map[string]any{
				"username": "tmdb",
				"source":   "tmdb",
				"tmdbId":   id,
				"collection": map[string]any{
					"name":        colName,
					"description": overview,
					"banner":      banner,
					"movieCount":  len(movies),
					"movies":      movies,
				},
			}
		}(i, name)
	}
	wg.Wait()

	entries := make([]map[string]any, 0, len(slots))
	for _, slot := range slots {
		if slot != nil {
			entries = append(entries, slot)
		}
	}
	if len(entries) > 0 { // never cache an empty result (TMDB hiccup)
		tmdbFeaturedCache.Store(adminMode, tmdbFeaturedCacheEntry{entries: entries, at: time.Now()})
	}
	writeJSON(w, http.StatusOK, entries)
}
