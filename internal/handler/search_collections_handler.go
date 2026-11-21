package handler

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"soulstash/internal/platform/mongodb"

	"go.mongodb.org/mongo-driver/v2/bson"
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
	"Captain America Collection",
	"Thor Collection",
	"Spider-Man Collection",
	"The Amazing Spider-Man Collection",
	"Deadpool Collection",
	"X-Men Collection",
	"Wolverine Collection",
	"Batman Collection",
	"Transformers Collection",
	"Planet of the Apes Collection",
	"Mad Max Collection",
	"Die Hard Collection",
	"The Bourne Collection",
	"Bad Boys Collection",
	"Men in Black Collection",
	"Ghostbusters Collection",
	"Rambo Collection",
	"The Expendables Collection",
	"Ocean's Collection",
	"The Twilight Saga",
	"The Maze Runner Collection",
	"Jumanji Collection",
	"MonsterVerse Collection",
	"A Quiet Place Collection",
	"Insidious Collection",
	"Saw Collection",
	"Scream Collection",
	"Halloween Collection",
	"A Nightmare on Elm Street Collection",
	"Final Destination Collection",
	"The Purge Collection",
	"Knives Out Collection",
	"Kingsman Collection",
	"Sonic the Hedgehog Collection",
	"Avatar Collection",
	"Dune Collection",
	"Blade Runner Collection",
	"Gladiator Collection",
	"National Treasure Collection",
	"Sherlock Holmes Collection",
	"The Hangover Collection",
	"Top Gun Collection",
	"Cars Collection",
	"Monsters, Inc. Collection",
	"Finding Nemo Collection",
	"The Incredibles Collection",
	"Madagascar Collection",
	"Hotel Transylvania Collection",
	"Minions Collection",
	"The Lion King Collection",
	"Frozen Collection",
	"The Chronicles of Narnia Collection",
	"The Mummy Collection",
	"Fifty Shades Collection",
	"The Karate Kid Collection",
	"Creed Collection",
	"The Equalizer Collection",
	"Jack Reacher Collection",
	"Doctor Strange Collection",
	"Black Panther Collection",
	"Ant-Man Collection",
	"Aquaman Collection",
	"Pacific Rim Collection",
	"The Transporter Collection",
	"Taken Collection",
	"Now You See Me Collection",
	"Sing Collection",
	"The Secret Life of Pets Collection",
	"Puss in Boots Collection",
	"The Hobbit Collection",
	"Star Trek Collection",
	"Godzilla Collection",
	"King Kong Collection",
	"Predator Collection",
	"RoboCop Collection",
	"Blade Collection",
	"Underworld Collection",
	"Resident Evil Collection",
	"Hellboy Collection",
	"Superman Collection",
	"Wonder Woman Collection",
	"Venom Collection",
	"Kill Bill Collection",
	"Lethal Weapon Collection",
	"Rush Hour Collection",
	"Beverly Hills Cop Collection",
	"Paddington Collection",
	"Trolls Collection",
	"The Croods Collection",
	"Annabelle Collection",
	"Friday the 13th Collection",
	"Child's Play Collection",
	"Paranormal Activity Collection",
	"Evil Dead Collection",
	"Cloverfield Collection",
	"The Divergent Series",
	"Percy Jackson Collection",
	"Fantastic Beasts Collection",
	"G.I. Joe Collection",
	"Tron Collection",
	"Dragon Ball Collection",
	"Pokemon Collection",
	"One Piece Film Collection",
	"Death Note Collection",
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

// SearchTMDBCollections searches both TMDB collections and Letterboxd collections by name/query
// for the navbar "Collections" tab. Letterboxd collections are served from MongoDB (cached).
func (h *ContentHandler) SearchTMDBCollections(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}})
		return
	}
	adminMode := h.searchAdminMode(r)
	ctx := r.Context()

	var letterboxdMatches []map[string]any

	// 1. Search Letterboxd collections from MongoDB (Saved & Cached)
	if h.db != nil {
		h.EnsureLetterboxdSeeded(ctx)
		coll := h.db.Collection(mongodb.CollectionLetterboxd)
		filter := bson.M{
			"$or": []bson.M{
				{"name": bson.M{"$regex": regexp.QuoteMeta(query), "$options": "i"}},
				{"description": bson.M{"$regex": regexp.QuoteMeta(query), "$options": "i"}},
				{"slug": bson.M{"$regex": regexp.QuoteMeta(query), "$options": "i"}},
			},
		}
		cursor, err := coll.Find(ctx, filter)
		if err == nil {
			var docs []bson.M
			if err := cursor.All(ctx, &docs); err == nil {
				for _, doc := range docs {
					slug, _ := doc["slug"].(string)
					name, _ := doc["name"].(string)
					desc, _ := doc["description"].(string)
					banner, _ := doc["banner"].(string)
					mCount := int(getFloat(doc["movieCount"]))
					if mList, ok := doc["movies"].([]any); ok && mCount == 0 {
						mCount = len(mList)
					}
					letterboxdMatches = append(letterboxdMatches, map[string]any{
						"id":            "letterboxd-" + slug,
						"slug":          slug,
						"name":          name,
						"title":         name,
						"overview":      desc,
						"description":   desc,
						"poster_path":   banner,
						"backdrop_path": banner,
						"item_count":    mCount,
						"source":        "letterboxd",
						"publisher":     "Letterboxd",
						"media_type":    "Collection",
					})
				}
			}
		}
	}

	// 2. Fallback check on memory seed if DB is not available or returned none
	if len(letterboxdMatches) == 0 {
		cols := loadLetterboxdCollections()
		for _, c := range cols {
			colData, _ := c["collection"].(map[string]any)
			cName, _ := colData["name"].(string)
			cDesc, _ := colData["description"].(string)
			cSlug, _ := c["slug"].(string)
			cBanner, _ := colData["banner"].(string)
			cCount := int(getFloat(colData["movieCount"]))
			if strings.Contains(strings.ToLower(cName), strings.ToLower(query)) ||
				strings.Contains(strings.ToLower(cDesc), strings.ToLower(query)) {
				letterboxdMatches = append(letterboxdMatches, map[string]any{
					"id":            "letterboxd-" + cSlug,
					"slug":          cSlug,
					"name":          cName,
					"title":         cName,
					"overview":      cDesc,
					"description":   cDesc,
					"poster_path":   cBanner,
					"backdrop_path": cBanner,
					"item_count":    cCount,
					"source":        "letterboxd",
					"publisher":     "Letterboxd",
					"media_type":    "Collection",
				})
			}
		}
	}

	// 3. Search TMDB collections
	data, err := h.tmdbClient.Get(ctx, "/3/search/collection", map[string]string{
		"query":         query,
		"include_adult": "false",
		"language":      "en-US",
		"page":          "1",
	})

	var tmdbResults []map[string]any
	if err == nil && data != nil {
		raw, _ := data["results"].([]any)
		if len(raw) > 12 {
			raw = raw[:12]
		}
		slots := make([]map[string]any, len(raw))
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
				detail := h.tmdbCollectionDetail(ctx, id)
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
				slots[idx] = map[string]any{
					"id":            id,
					"tmdbId":        id,
					"name":          name,
					"title":         name,
					"overview":      overview,
					"description":   overview,
					"poster_path":   tmdbCollectionPoster(detail),
					"backdrop_path": detail["backdrop_path"],
					"item_count":    len(tmdbCollectionParts(detail)),
					"source":        "tmdb",
					"publisher":     "TMDB",
					"media_type":    "Collection",
				}
			}(i, item)
		}
		wg.Wait()
		for _, s := range slots {
			if s != nil {
				tmdbResults = append(tmdbResults, s)
			}
		}
	}

	// 4. If neither TMDB nor MongoDB matched anything and query length >= 3,
	// dynamically resolve from TMDB / Letterboxd topic, cache in MongoDB, and include in search results!
	if len(letterboxdMatches) == 0 && len(tmdbResults) == 0 && len([]rune(query)) >= 3 {
		dynamicCol, err := h.ResolveDynamicLetterboxdCollection(ctx, query)
		if err == nil && dynamicCol != nil {
			cMap, _ := dynamicCol["collection"].(map[string]any)
			slug, _ := dynamicCol["slug"].(string)
			name, _ := cMap["name"].(string)
			desc, _ := cMap["description"].(string)
			banner, _ := cMap["banner"].(string)
			mCount := int(getFloat(cMap["movieCount"]))
			letterboxdMatches = append(letterboxdMatches, map[string]any{
				"id":            "letterboxd-" + slug,
				"slug":          slug,
				"name":          name,
				"title":         name,
				"overview":      desc,
				"description":   desc,
				"poster_path":   banner,
				"backdrop_path": banner,
				"item_count":    mCount,
				"source":        "letterboxd",
				"publisher":     "Letterboxd",
				"media_type":    "Collection",
			})
		}
	}

	// Merge Letterboxd results and TMDB results
	allResults := append(letterboxdMatches, tmdbResults...)
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": allResults})
}

func (h *ContentHandler) resolveCollectionSlot(ctx context.Context, id int, adminMode int) map[string]any {
	detail := h.tmdbCollectionDetail(ctx, id)
	if detail == nil || !h.tmdbCollectionVisible(detail, adminMode) {
		return nil
	}
	parts := tmdbCollectionParts(detail)
	if len(parts) == 0 {
		return nil
	}
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
	return map[string]any{
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
}

func (h *ContentHandler) resolveCollectionByName(ctx context.Context, name string, adminMode int) map[string]any {
	found, err := h.tmdbClient.Get(ctx, "/3/search/collection", map[string]string{
		"query":         name,
		"include_adult": "false",
		"language":      "en-US",
		"page":          "1",
	})
	if err != nil || found == nil {
		return nil
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
		return nil
	}
	id := int(getFloat(pick["id"]))
	return h.resolveCollectionSlot(ctx, id, adminMode)
}

// GetFeaturedTMDBCollections returns curated TMDB franchises in the same shape as
// /api/collections/published entries ({username: "tmdb", collection: {...}}), so the
// Lists page can show them next to user collections.
// Supports pagination (?page=1&limit=12) with infinite dynamic collection discovery from TMDB!
func (h *ContentHandler) GetFeaturedTMDBCollections(w http.ResponseWriter, r *http.Request) {
	adminMode := h.searchAdminMode(r)
	pageStr := strings.TrimSpace(r.URL.Query().Get("page"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))

	// Non-paginated request: backward compatibility (loads initial pool of featured franchises)
	if pageStr == "" && limitStr == "" {
		cacheKey := fmt.Sprintf("all:%d", adminMode)
		if v, ok := tmdbFeaturedCache.Load(cacheKey); ok {
			if entry := v.(tmdbFeaturedCacheEntry); time.Since(entry.at) < tmdbFeaturedTTL {
				writeJSON(w, http.StatusOK, entry.entries)
				return
			}
		}
		curatedMax := len(tmdbFeaturedCollectionNames)
		if curatedMax > 60 {
			curatedMax = 60
		}
		slots := make([]map[string]any, curatedMax)
		sem := make(chan struct{}, 10)
		var wg sync.WaitGroup
		for i := 0; i < curatedMax; i++ {
			wg.Add(1)
			go func(idx int, name string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				slots[idx] = h.resolveCollectionByName(r.Context(), name, adminMode)
			}(i, tmdbFeaturedCollectionNames[i])
		}
		wg.Wait()

		entries := make([]map[string]any, 0, len(slots))
		for _, slot := range slots {
			if slot != nil {
				entries = append(entries, slot)
			}
		}
		if len(entries) > 0 {
			tmdbFeaturedCache.Store(cacheKey, tmdbFeaturedCacheEntry{entries: entries, at: time.Now()})
		}
		writeJSON(w, http.StatusOK, entries)
		return
	}

	// Paginated request: infinite scrolling support!
	page := 1
	limit := 12
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 40 {
		limit = l
	}

	cacheKey := fmt.Sprintf("p:%d:l:%d:m:%d", page, limit, adminMode)
	if v, ok := tmdbFeaturedCache.Load(cacheKey); ok {
		if entry := v.(tmdbFeaturedCacheEntry); time.Since(entry.at) < tmdbFeaturedTTL {
			writeJSON(w, http.StatusOK, entry.entries)
			return
		}
	}

	offset := (page - 1) * limit
	var entries []map[string]any

	if offset < len(tmdbFeaturedCollectionNames) {
		end := offset + limit
		if end > len(tmdbFeaturedCollectionNames) {
			end = len(tmdbFeaturedCollectionNames)
		}
		batch := tmdbFeaturedCollectionNames[offset:end]
		slots := make([]map[string]any, len(batch))
		sem := make(chan struct{}, 10)
		var wg sync.WaitGroup
		for i, name := range batch {
			wg.Add(1)
			go func(idx int, name string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				slots[idx] = h.resolveCollectionByName(r.Context(), name, adminMode)
			}(i, name)
		}
		wg.Wait()
		for _, slot := range slots {
			if slot != nil {
				entries = append(entries, slot)
			}
		}
	}

	// If offset reached end of curated list OR not enough entries returned, discover dynamically from TMDB
	if len(entries) < limit {
		curatedPages := (len(tmdbFeaturedCollectionNames) + limit - 1) / limit
		discoverPage := 1
		if page > curatedPages {
			discoverPage = page - curatedPages + 1
		}
		searchQueries := []string{"Collection", "Trilogy", "Saga", "Chronicles"}
		queryIdx := (discoverPage - 1) % len(searchQueries)
		tmdbApiPage := ((discoverPage - 1) / len(searchQueries)) + 1

		found, err := h.tmdbClient.Get(r.Context(), "/3/search/collection", map[string]string{
			"query":         searchQueries[queryIdx],
			"include_adult": "false",
			"language":      "en-US",
			"page":          strconv.Itoa(tmdbApiPage),
		})
		if err == nil && found != nil {
			list, _ := found["results"].([]any)
			sem := make(chan struct{}, 8)
			var wg sync.WaitGroup
			discoveredSlots := make([]map[string]any, len(list))
			for i, entry := range list {
				wg.Add(1)
				go func(idx int, raw any) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					item, ok := raw.(map[string]any)
					if !ok || getFloat(item["id"]) == 0 {
						return
					}
					id := int(getFloat(item["id"]))
					discoveredSlots[idx] = h.resolveCollectionSlot(r.Context(), id, adminMode)
				}(i, entry)
			}
			wg.Wait()

			seenIds := make(map[int]bool)
			for _, e := range entries {
				if id, ok := e["tmdbId"].(int); ok {
					seenIds[id] = true
				}
			}
			for _, slot := range discoveredSlots {
				if slot != nil {
					id, _ := slot["tmdbId"].(int)
					if !seenIds[id] {
						seenIds[id] = true
						entries = append(entries, slot)
						if len(entries) >= limit {
							break
						}
					}
				}
			}
		}
	}

	if len(entries) > 0 {
		tmdbFeaturedCache.Store(cacheKey, tmdbFeaturedCacheEntry{entries: entries, at: time.Now()})
	}
	writeJSON(w, http.StatusOK, entries)
}
