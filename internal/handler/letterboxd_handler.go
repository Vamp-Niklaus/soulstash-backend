package handler

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"soulstash/internal/platform/mongodb"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

//go:embed data/letterboxd_featured.json
var letterboxdFeaturedJSON []byte

var (
	letterboxdOnce        sync.Once
	letterboxdCollections []map[string]any
	letterboxdSeededOnce  sync.Once
	globalLetterboxdDB    *mongo.Database
	globalLetterboxdLock  sync.RWMutex
)

func setGlobalLetterboxdDB(db *mongo.Database) {
	globalLetterboxdLock.Lock()
	defer globalLetterboxdLock.Unlock()
	globalLetterboxdDB = db
}

func loadLetterboxdCollections() []map[string]any {
	letterboxdOnce.Do(func() {
		if len(letterboxdFeaturedJSON) > 0 {
			_ = json.Unmarshal(letterboxdFeaturedJSON, &letterboxdCollections)
		}
	})
	return letterboxdCollections
}

// EnsureLetterboxdSeeded checks if MongoDB has Letterboxd collections.
// If 0 documents are found, it seeds the initial verified collections into MongoDB.
func (h *ContentHandler) EnsureLetterboxdSeeded(ctx context.Context) {
	if h.db == nil {
		return
	}
	letterboxdSeededOnce.Do(func() {
		coll := h.db.Collection(mongodb.CollectionLetterboxd)
		count, err := coll.CountDocuments(ctx, bson.M{})
		if err != nil || count > 0 {
			return
		}

		seeds := loadLetterboxdCollections()
		if len(seeds) == 0 {
			return
		}

		docs := make([]any, 0, len(seeds))
		now := time.Now()
		for _, seed := range seeds {
			slug, _ := seed["slug"].(string)
			colMap, _ := seed["collection"].(map[string]any)
			if colMap == nil {
				colMap = seed
			}
			name, _ := colMap["name"].(string)
			desc, _ := colMap["description"].(string)
			banner, _ := colMap["banner"].(string)
			movies, _ := colMap["movies"].([]any)
			if slug == "" {
				slug = slugify(name)
			}

			doc := bson.M{
				"_id":         slug,
				"slug":        slug,
				"name":        name,
				"description": desc,
				"banner":      banner,
				"movieCount":  len(movies),
				"movies":      movies,
				"source":      "letterboxd",
				"username":    "letterboxd",
				"createdAt":   now,
				"updatedAt":   now,
			}
			docs = append(docs, doc)
		}

		if len(docs) > 0 {
			_, _ = coll.InsertMany(ctx, docs)
		}
	})
}

// GetLetterboxdCollections returns Letterboxd collections.
// If MongoDB is connected, it serves directly from MongoDB (cached).
// Never hits Letterboxd directly if cached.
func (h *ContentHandler) GetLetterboxdCollections(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	ctx := r.Context()

	if h.db != nil {
		h.EnsureLetterboxdSeeded(ctx)
		coll := h.db.Collection(mongodb.CollectionLetterboxd)

		filter := bson.M{}
		if q != "" {
			filter = bson.M{
				"$or": []bson.M{
					{"name": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
					{"description": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
					{"slug": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
				},
			}
		}

		opts := options.Find().SetSort(bson.M{"updatedAt": -1})
		cursor, err := coll.Find(ctx, filter, opts)
		if err == nil {
			var docs []bson.M
			if err := cursor.All(ctx, &docs); err == nil && len(docs) > 0 {
				results := make([]map[string]any, 0, len(docs))
				for _, doc := range docs {
					slug, _ := doc["slug"].(string)
					name, _ := doc["name"].(string)
					desc, _ := doc["description"].(string)
					banner, _ := doc["banner"].(string)
					movies := doc["movies"]
					mCount := int(getFloat(doc["movieCount"]))
					if mList, ok := movies.([]any); ok && mCount == 0 {
						mCount = len(mList)
					}

					results = append(results, map[string]any{
						"username": "letterboxd",
						"source":   "letterboxd",
						"slug":     slug,
						"collection": map[string]any{
							"name":        name,
							"slug":        slug,
							"description": desc,
							"banner":      banner,
							"movieCount":  mCount,
							"movies":      movies,
						},
					})
				}
				writeJSON(w, http.StatusOK, results)
				return
			}
		}
	}

	// Fallback to memory seeds
	cols := loadLetterboxdCollections()
	if q != "" {
		filtered := make([]map[string]any, 0)
		for _, c := range cols {
			colData, _ := c["collection"].(map[string]any)
			cName, _ := colData["name"].(string)
			cDesc, _ := colData["description"].(string)
			if strings.Contains(strings.ToLower(cName), strings.ToLower(q)) ||
				strings.Contains(strings.ToLower(cDesc), strings.ToLower(q)) {
				filtered = append(filtered, c)
			}
		}
		writeJSON(w, http.StatusOK, filtered)
		return
	}

	if cols == nil {
		cols = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, cols)
}

// GetLetterboxdCollection returns a single Letterboxd collection by slug or name.
// First serves from MongoDB cache (0ms). If cache miss, dynamically resolves and caches in MongoDB.
func (h *ContentHandler) GetLetterboxdCollection(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		slug = strings.TrimSpace(r.URL.Query().Get("slug"))
	}
	if slug == "" {
		slug = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if slug == "" {
		writeError(w, http.StatusBadRequest, "Letterboxd collection slug or name required")
		return
	}

	col := h.FindOrResolveLetterboxdCollection(r.Context(), slug)
	if col == nil {
		writeError(w, http.StatusNotFound, "Letterboxd collection not found")
		return
	}
	writeJSON(w, http.StatusOK, col)
}

// FindOrResolveLetterboxdCollection searches MongoDB first. If cache miss, checks seed;
// if still not found, dynamically resolves movies via TMDB, stores in MongoDB, and returns!
func (h *ContentHandler) FindOrResolveLetterboxdCollection(ctx context.Context, identifier string) map[string]any {
	if identifier == "" {
		return nil
	}
	target := strings.TrimSpace(identifier)

	// 1. Check MongoDB first (Saved & Cached)
	if h.db != nil {
		h.EnsureLetterboxdSeeded(ctx)
		coll := h.db.Collection(mongodb.CollectionLetterboxd)
		filter := bson.M{
			"$or": []bson.M{
				{"_id": target},
				{"slug": strings.ToLower(target)},
				{"name": bson.M{"$regex": "^" + regexp.QuoteMeta(target) + "$", "$options": "i"}},
				{"slug": strings.ToLower(slugify(target))},
			},
		}
		var doc bson.M
		if err := coll.FindOne(ctx, filter).Decode(&doc); err == nil && doc != nil {
			slug, _ := doc["slug"].(string)
			name, _ := doc["name"].(string)
			desc, _ := doc["description"].(string)
			banner, _ := doc["banner"].(string)
			movies := doc["movies"]
			mCount := int(getFloat(doc["movieCount"]))
			if mList, ok := movies.([]any); ok && mCount == 0 {
				mCount = len(mList)
			}
			return map[string]any{
				"username": "letterboxd",
				"source":   "letterboxd",
				"slug":     slug,
				"collection": map[string]any{
					"name":        name,
					"slug":        slug,
					"description": desc,
					"banner":      banner,
					"movieCount":  mCount,
					"movies":      movies,
				},
			}
		}
	}

	// 2. Check in-memory seed collections
	cols := loadLetterboxdCollections()
	for _, entry := range cols {
		slug, _ := entry["slug"].(string)
		if strings.EqualFold(slug, target) || strings.EqualFold(slug, slugify(target)) {
			h.saveLetterboxdDocToDB(ctx, entry)
			return entry
		}
		if col, ok := entry["collection"].(map[string]any); ok {
			colSlug, _ := col["slug"].(string)
			if strings.EqualFold(colSlug, target) || strings.EqualFold(colSlug, slugify(target)) {
				h.saveLetterboxdDocToDB(ctx, entry)
				return entry
			}
			colName, _ := col["name"].(string)
			if strings.EqualFold(colName, target) || strings.EqualFold(strings.ReplaceAll(colName, " ", "-"), target) {
				h.saveLetterboxdDocToDB(ctx, entry)
				return entry
			}
		}
	}

	// 3. Cache Miss: Dynamically resolve from TMDB / Letterboxd topic, store into MongoDB, and serve!
	resolved, err := h.ResolveDynamicLetterboxdCollection(ctx, target)
	if err == nil && resolved != nil {
		return resolved
	}

	return nil
}

// ResolveDynamicLetterboxdCollection dynamically builds a curated Letterboxd collection
// by resolving movies from TMDB by topic/name, stores it permanently in MongoDB, and returns it.
func (h *ContentHandler) ResolveDynamicLetterboxdCollection(ctx context.Context, topic string) (map[string]any, error) {
	cleanQuery := strings.TrimSpace(topic)
	if len(cleanQuery) < 2 {
		return nil, fmt.Errorf("topic too short")
	}

	// Clean out common prefixes/suffixes for TMDB search
	searchQuery := regexp.MustCompile(`(?i)(collection|list|curated|ranked|top\s*\d+)`).ReplaceAllString(cleanQuery, "")
	searchQuery = strings.TrimSpace(searchQuery)
	if searchQuery == "" {
		searchQuery = cleanQuery
	}

	found, err := h.tmdbClient.Get(ctx, "/3/search/movie", map[string]string{
		"query":         searchQuery,
		"include_adult": "false",
		"language":      "en-US",
		"page":          "1",
	})
	if err != nil || found == nil {
		return nil, fmt.Errorf("tmdb search error")
	}

	rawList, _ := found["results"].([]any)
	if len(rawList) == 0 {
		return nil, fmt.Errorf("no movies found")
	}

	if len(rawList) > 20 {
		rawList = rawList[:20]
	}

	movies := make([]any, 0, len(rawList))
	var banner string
	now := time.Now().UTC().Format(time.RFC3339)

	for _, raw := range rawList {
		item, ok := raw.(map[string]any)
		if !ok || getFloat(item["id"]) == 0 {
			continue
		}
		id := int(getFloat(item["id"]))
		title, _ := item["title"].(string)
		posterPath, _ := item["poster_path"].(string)
		backdropPath, _ := item["backdrop_path"].(string)
		relDate, _ := item["release_date"].(string)
		voteAvg := getFloat(item["vote_average"])

		if banner == "" && backdropPath != "" {
			banner = "https://image.tmdb.org/t/p/w1280" + backdropPath
		}

		movies = append(movies, map[string]any{
			"movieId":        id,
			"seriesId":       nil,
			"id":             id,
			"title":          title,
			"poster_path":    posterPath,
			"backdrop_path":  backdropPath,
			"release_date":   relDate,
			"first_air_date": "",
			"media_type":     "Movie",
			"isAnime":        false,
			"vote_average":   voteAvg,
			"imdb_id":        "",
			"imdb_rating":    nil,
			"addedAt":        now,
		})
	}

	if len(movies) == 0 {
		return nil, fmt.Errorf("no valid movies resolved")
	}

	colSlug := slugify(cleanQuery)
	colName := cleanQuery
	if !strings.Contains(strings.ToLower(colName), "collection") && !strings.Contains(strings.ToLower(colName), "list") {
		colName = colName + " (Letterboxd Curated)"
	}

	newEntry := map[string]any{
		"username": "letterboxd",
		"source":   "letterboxd",
		"slug":     colSlug,
		"collection": map[string]any{
			"name":        colName,
			"slug":        colSlug,
			"description": fmt.Sprintf("Curated Letterboxd collection for %s, enriched with TMDB metadata.", cleanQuery),
			"banner":      banner,
			"movieCount":  len(movies),
			"movies":      movies,
		},
	}

	// Persist into MongoDB cache
	h.saveLetterboxdDocToDB(ctx, newEntry)

	return newEntry, nil
}

// saveLetterboxdDocToDB stores a Letterboxd collection document into MongoDB
func (h *ContentHandler) saveLetterboxdDocToDB(ctx context.Context, entry map[string]any) {
	if h.db == nil {
		return
	}
	slug, _ := entry["slug"].(string)
	col, _ := entry["collection"].(map[string]any)
	if col == nil {
		return
	}
	name, _ := col["name"].(string)
	desc, _ := col["description"].(string)
	banner, _ := col["banner"].(string)
	movies := col["movies"]
	mCount := int(getFloat(col["movieCount"]))
	if mList, ok := movies.([]any); ok && mCount == 0 {
		mCount = len(mList)
	}

	if slug == "" {
		slug = slugify(name)
	}

	coll := h.db.Collection(mongodb.CollectionLetterboxd)
	filter := bson.M{"_id": slug}
	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"slug":        slug,
			"name":        name,
			"description": desc,
			"banner":      banner,
			"movieCount":  mCount,
			"movies":      movies,
			"source":      "letterboxd",
			"username":    "letterboxd",
			"updatedAt":   now,
		},
		"$setOnInsert": bson.M{
			"createdAt": now,
		},
	}
	opts := options.UpdateOne().SetUpsert(true)
	_, _ = coll.UpdateOne(ctx, filter, update, opts)
}

// ImportCustomLetterboxd imports any custom Letterboxd list by URL or name with film titles,
// enriches every film via TMDB, and saves the collection into MongoDB.
func (h *ContentHandler) ImportCustomLetterboxd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Films       []struct {
			Title string `json:"title"`
			Year  string `json:"year"`
		} `json:"films"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Collection name is required")
		return
	}

	ctx := r.Context()
	movies := make([]any, 0, len(body.Films))
	var banner string
	now := time.Now().UTC().Format(time.RFC3339)

	// Enrich each film via TMDB
	for _, film := range body.Films {
		title := strings.TrimSpace(film.Title)
		if title == "" {
			continue
		}
		params := map[string]string{
			"query":         title,
			"include_adult": "false",
			"language":      "en-US",
			"page":          "1",
		}
		if film.Year != "" {
			params["year"] = film.Year
		}
		found, err := h.tmdbClient.Get(ctx, "/3/search/movie", params)
		if err == nil && found != nil {
			if list, ok := found["results"].([]any); ok && len(list) > 0 {
				if item, ok := list[0].(map[string]any); ok && getFloat(item["id"]) > 0 {
					id := int(getFloat(item["id"]))
					mTitle, _ := item["title"].(string)
					posterPath, _ := item["poster_path"].(string)
					backdropPath, _ := item["backdrop_path"].(string)
					relDate, _ := item["release_date"].(string)
					voteAvg := getFloat(item["vote_average"])

					if banner == "" && backdropPath != "" {
						banner = "https://image.tmdb.org/t/p/w1280" + backdropPath
					}

					movies = append(movies, map[string]any{
						"movieId":        id,
						"seriesId":       nil,
						"id":             id,
						"title":          mTitle,
						"poster_path":    posterPath,
						"backdrop_path":  backdropPath,
						"release_date":   relDate,
						"first_air_date": "",
						"media_type":     "Movie",
						"isAnime":        false,
						"vote_average":   voteAvg,
						"imdb_id":        "",
						"imdb_rating":    nil,
						"addedAt":        now,
					})
				}
			}
		}
	}

	slug := slugify(name)
	newEntry := map[string]any{
		"username": "letterboxd",
		"source":   "letterboxd",
		"slug":     slug,
		"collection": map[string]any{
			"name":        name,
			"slug":        slug,
			"description": body.Description,
			"banner":      banner,
			"movieCount":  len(movies),
			"movies":      movies,
			"url":         body.URL,
		},
	}

	h.saveLetterboxdDocToDB(ctx, newEntry)

	writeJSON(w, http.StatusOK, newEntry)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	return strings.Trim(re.ReplaceAllString(s, "-"), "-")
}

// FindLetterboxdCollection finds a Letterboxd collection by slug or name
func FindLetterboxdCollection(identifier string) map[string]any {
	if identifier == "" {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(identifier))

	// 1. Check MongoDB first if DB is available
	globalLetterboxdLock.RLock()
	db := globalLetterboxdDB
	globalLetterboxdLock.RUnlock()

	if db != nil {
		coll := db.Collection(mongodb.CollectionLetterboxd)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		filter := bson.M{
			"$or": []bson.M{
				{"_id": target},
				{"slug": target},
				{"slug": slugify(target)},
				{"name": bson.M{"$regex": "^" + regexp.QuoteMeta(target) + "$", "$options": "i"}},
			},
		}
		var doc bson.M
		if err := coll.FindOne(ctx, filter).Decode(&doc); err == nil && doc != nil {
			slug, _ := doc["slug"].(string)
			name, _ := doc["name"].(string)
			desc, _ := doc["description"].(string)
			banner, _ := doc["banner"].(string)
			movies := doc["movies"]
			mCount := int(getFloat(doc["movieCount"]))
			if mList, ok := movies.([]any); ok && mCount == 0 {
				mCount = len(mList)
			}
			return map[string]any{
				"username": "letterboxd",
				"source":   "letterboxd",
				"slug":     slug,
				"collection": map[string]any{
					"name":        name,
					"slug":        slug,
					"description": desc,
					"banner":      banner,
					"movieCount":  mCount,
					"movies":      movies,
				},
			}
		}
	}

	// 2. Check in-memory seeds
	cols := loadLetterboxdCollections()
	for _, entry := range cols {
		slug, _ := entry["slug"].(string)
		if strings.EqualFold(slug, target) || strings.EqualFold(slug, slugify(target)) {
			return entry
		}
		if col, ok := entry["collection"].(map[string]any); ok {
			colSlug, _ := col["slug"].(string)
			if strings.EqualFold(colSlug, target) || strings.EqualFold(colSlug, slugify(target)) {
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
