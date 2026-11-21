package handler

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	letterboxdOnce                 sync.Once
	letterboxdCollections          []map[string]any
	letterboxdSeededOnce           sync.Once
	globalLetterboxdDB             *mongo.Database
	globalLetterboxdContentHandler *ContentHandler
	globalLetterboxdLock           sync.RWMutex
)

func setGlobalLetterboxdDB(db *mongo.Database) {
	globalLetterboxdLock.Lock()
	defer globalLetterboxdLock.Unlock()
	globalLetterboxdDB = db
}

func setGlobalLetterboxdContentHandler(h *ContentHandler) {
	globalLetterboxdLock.Lock()
	defer globalLetterboxdLock.Unlock()
	globalLetterboxdContentHandler = h
}

var letterboxdCuratedThemes = []struct {
	Slug        string
	Name        string
	Description string
	Query       string
}{
	{"letterboxd-top-250", "Letterboxd Official Top 250", "The 250 highest-rated films of all time on Letterboxd.", "top rated cinema"},
	{"letterboxd-popular", "Letterboxd Most Popular Films", "The most logged and watched movies by the global Letterboxd community.", "popular cinema"},
	{"best-of-a24", "Best of A24 Ranked", "Every genre-defying, acclaimed release from indie juggernaut A24.", "A24"},
	{"studio-ghibli", "Studio Ghibli Complete Collection", "Hayao Miyazaki and Isao Takahata's timeless hand-drawn animated classics.", "Miyazaki Ghibli"},
	{"sight-and-sound-greatest", "Sight & Sound Critics Greatest Films", "The decennial international critics poll of the greatest cinema in history.", "greatest cinema masterpieces"},
	{"1001-movies-must-see", "1001 Movies You Must See Before You Die", "The definitive, essential cinematic milestones everyone must experience once.", "classic masterpiece"},
	{"top-scifi-masterpieces", "Top Sci-Fi Masterpieces", "Mind-bending journeys through space, time, dystopian futures, and artificial intellect.", "sci-fi space dystopia"},
	{"top-horror-masterpieces", "Top Horror Masterpieces", "The terrifying, atmospheric, and psychological horror benchmarks rated highest on Letterboxd.", "horror psychological"},
	{"fan-favorites-2024-2025", "Letterboxd Fan Favorites (2024–2025)", "The defining modern theatrical sensations, festival darlings, and viral triumphs.", "award winning movies"},
	{"neo-noir-cyberpunk", "Neo-Noir & Cyberpunk Essentials", "Rain-slicked streets, cynical detectives, and high-tech dystopias.", "cyberpunk noir detective"},
	{"palme-dor-cannes", "Palme d'Or & Festival Winners", "Prestigious Cannes and European film festival triumphs celebrated by cinephiles.", "Cannes Palme d'Or"},
	{"academy-best-picture", "Best Picture Winners Ranked", "Oscar Best Picture winners ranked by Letterboxd audience esteem.", "Academy Award winner"},
	{"90s-cult-classics", "90s Cult Classics & Benchmarks", "The unforgettable, rebellious, and boundary-pushing gems of the 1990s.", "cult classic 1990s"},
	{"tarantino-favorites", "Quentin Tarantino's Cinema Universe", "Electrifying dialogue, nonlinear storytelling, and hyper-stylized cinema.", "Tarantino crime cinema"},
	{"christopher-nolan-mindbenders", "Christopher Nolan & Mind-Benders", "Time puzzles, practical spectacles, and mind-bending conceptual thrillers.", "mind bending concept thriller"},
	{"scorsese-essentials", "Martin Scorsese Essential Cinema", "Gritty street epics, character studies, and cinematic masterpieces from the maestro.", "Scorsese crime mafia"},
	{"modern-horror-renaissance", "Modern Horror Renaissance", "Atmospheric dread, folk terror, and visionary modern horror auteurs.", "modern horror folk dread"},
	{"asian-cinema-masterpieces", "Asian Cinema Masterpieces", "Groundbreaking masterpieces from South Korea, Japan, Hong Kong, and Taiwan.", "Korean Japanese thriller cinema"},
	{"french-new-wave", "French New Wave & European Arthouse", "Revolutionary jump cuts, poetic realism, and auteur cinema.", "French European arthouse"},
	{"anime-film-masterpieces", "Anime Film Masterpieces", "Iconic anime features from Makoto Shinkai, Satoshi Kon, and Mamoru Hosoda.", "anime feature film masterpiece"},
	{"psychological-thrillers", "Psychological Thrillers Ranked", "Twisted games, unreliable narrators, and claustrophobic tension.", "psychological thriller mystery"},
	{"coming-of-age-masterpieces", "Coming-of-Age Cinema", "Bittersweet nostalgia, youthful discovery, and formative life lessons.", "coming of age youth drama"},
	{"indie-gems-ranked", "Top Rated Indie Gems", "Low-budget triumphs that captured hearts with raw emotion and original voices.", "independent Sundance festival film"},
	{"dystopian-post-apocalyptic", "Dystopian & Post-Apocalyptic", "Desolate wastelands, totalitarian regimes, and survival against all odds.", "post-apocalyptic survival dystopia"},
	{"epic-historical-war", "Epic Historical & War Masterpieces", "Sweeping battlefields, ancient empires, and deeply human wartime stories.", "historical war epic battle"},
	{"whodunit-mystery", "Letterboxd Whodunits & Mysteries", "Locked-room puzzles, sharp detectives, and shocking twists.", "whodunit murder mystery detective"},
	{"action-cinema-hall-of-fame", "Action Cinema Hall of Fame", "Unrelenting stunts, masterclass choreography, and adrenaline-pumping classics.", "action martial arts stunt"},
	{"surrealist-cinema", "Surrealist & Dream Arthouse", "Lynchian dreamscapes, abstract visions, and cinema of the subconscious.", "surrealist dream film"},
	{"cozy-comfort-movies", "Letterboxd Comfort Cinema", "Warm, uplifting, and beautifully gentle films to soothe the soul.", "feel-good comfort wholesome comedy"},
	{"dark-comedy-benchmarks", "Dark Comedy Benchmarks", "Morbid humor, sharp satire, and delightfully cynical laughs.", "dark comedy satire humor"},
	{"21st-century-masterpieces", "21st Century Masterpieces", "The absolute pinnacles of cinema released from 2000 to the present day.", "contemporary masterpiece cinema"},
	{"directorial-debuts", "Greatest Directorial Debuts", "Visionary first features that stunned audiences and announced legendary filmmakers.", "directorial debut breakthrough film"},
	{"space-odysseys", "Cosmic & Space Odysseys", "The infinite void, interstellar travel, and humanity's place in the universe.", "space exploration interstellar cosmic"},
	{"spaghetti-westerns", "Spaghetti Western Classics", "Sergio Leone, dusty showdowns, iconic score, and frontier vengeance.", "spaghetti western Sergio Leone showdown"},
	{"stop-motion-animation", "Stop-Motion Animation Gems", "Intricate puppetry, Laika, Aardman, and handmade cinematic wizardry.", "stop motion animation claymation"},
	{"heist-crime-thrillers", "High-Stakes Heist & Crime", "Meticulous plans, cunning thieves, safe-cracking, and breathless getaways.", "heist crime robbery crew"},
	{"poetic-romances", "Poetic & Heartfelt Romances", "Deep emotional connections, bittersweet longing, and sweeping love stories.", "romance love drama poetic"},
	{"time-travel-cinema", "Time Travel & Temporal Paradox", "Looped realities, butterfly effects, and temporal mechanics.", "time travel temporal paradox loop"},
	{"criterion-essentials", "The Criterion Collection Essentials", "Significant, restored, and historic cinema from around the world.", "criterion collection classic restored"},
	{"modern-sci-fi", "Modern Sci-Fi Renaissance", "Philosophical sci-fi, first contact, and cerebral speculative fiction.", "speculative fiction philosophical sci-fi"},
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
// Seeds verified collections and purges any legacy generated spotlights.
func (h *ContentHandler) EnsureLetterboxdSeeded(ctx context.Context) {
	if h.db == nil {
		return
	}
	letterboxdSeededOnce.Do(func() {
		coll := h.db.Collection(mongodb.CollectionLetterboxd)
		// Clean up any old dynamic or repetitive spotlights/themes generated into MongoDB!
		// Keep ONLY verified seed slugs in MongoDB.
		seedSlugs := []string{
			"letterboxd-top-250",
			"letterboxd-popular",
			"best-of-a24-ranked",
			"studio-ghibli-complete",
			"sight-and-sound-greatest",
			"1001-movies-before-you-die",
			"top-scifi-masterpieces",
			"top-horror-masterpieces",
			"fan-favorites-2024-2025",
		}
		_, _ = coll.DeleteMany(ctx, bson.M{
			"$or": []bson.M{
				{"slug": bson.M{"$nin": seedSlugs}},
				{"slug": bson.M{"$regex": "^letterboxd-spotlight-"}},
				{"name": bson.M{"$regex": "(?i)spotlight"}},
				{"name": bson.M{"$regex": "(?i)curated"}},
			},
		})

		seeds := loadLetterboxdCollections()
		if len(seeds) == 0 {
			return
		}

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
			if len(movies) == 0 {
				continue
			}

			filter := bson.M{"$or": []bson.M{{"_id": slug}, {"slug": slug}}}
			var existing bson.M
			if err := coll.FindOne(ctx, filter).Decode(&existing); err != nil {
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
				_, _ = coll.InsertOne(ctx, doc)
			} else {
				// If existing document has null or 0 movies, repair it!
				var existingCount int
				if mList, ok := existing["movies"].(bson.A); ok {
					existingCount = len(mList)
				} else if mList2, ok2 := existing["movies"].([]any); ok2 {
					existingCount = len(mList2)
				}
				if existingCount == 0 {
					_, _ = coll.UpdateOne(ctx, filter, bson.M{
						"$set": bson.M{
							"movies":     movies,
							"movieCount": len(movies),
							"name":       name,
							"banner":     banner,
							"updatedAt":  now,
						},
					})
				}
			}
		}
	})
}

// GetLetterboxdCollections returns Letterboxd collections.
// Supports endless pagination (?page=1&limit=10) and text search (?q=...).
// Serves directly from MongoDB cache with dynamic theme resolution on demand.
func (h *ContentHandler) GetLetterboxdCollections(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pageStr := strings.TrimSpace(r.URL.Query().Get("page"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))
	ctx := r.Context()

	page := 1
	limit := 10
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
		limit = l
	}

	if h.db != nil {
		h.EnsureLetterboxdSeeded(ctx)
		coll := h.db.Collection(mongodb.CollectionLetterboxd)

		seedSlugs := []string{
			"letterboxd-top-250",
			"letterboxd-popular",
			"best-of-a24-ranked",
			"studio-ghibli-complete",
			"sight-and-sound-greatest",
			"1001-movies-before-you-die",
			"top-scifi-masterpieces",
			"top-horror-masterpieces",
			"fan-favorites-2024-2025",
		}
		filter := bson.M{
			"slug": bson.M{"$in": seedSlugs},
		}
		if q != "" {
			filter["$or"] = []bson.M{
				{"name": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
				{"description": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
				{"slug": bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}},
			}
		}

		offset := (page - 1) * limit
		opts := options.Find().SetSort(bson.M{"createdAt": 1}).SetSkip(int64(offset)).SetLimit(int64(limit))
		cursor, err := coll.Find(ctx, filter, opts)
		if err == nil {
			var docs []bson.M
			_ = cursor.All(ctx, &docs)
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
				} else if mList2, ok2 := movies.(bson.A); ok2 && mCount == 0 {
					mCount = len(mList2)
				}
				// If movies is empty or null, check if seed has it!
				if mCount == 0 || movies == nil {
					for _, seed := range loadLetterboxdCollections() {
						sCol, _ := seed["collection"].(map[string]any)
						if sCol == nil {
							sCol = seed
						}
						sSlug, _ := seed["slug"].(string)
						if sSlug == "" {
							sSlug, _ = sCol["slug"].(string)
						}
						sName, _ := sCol["name"].(string)
						if strings.EqualFold(sSlug, slug) || strings.EqualFold(slugify(sName), slug) {
							if sMovies, ok := sCol["movies"].([]any); ok && len(sMovies) > 0 {
								movies = sMovies
								mCount = len(sMovies)
								_, _ = coll.UpdateOne(ctx, bson.M{"_id": slug}, bson.M{"$set": bson.M{"movies": sMovies, "movieCount": len(sMovies)}})
								break
							}
						}
					}
				}
				if movies == nil {
					movies = []any{}
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

			if len(results) > 0 || pageStr != "" {
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
	offset := (page - 1) * limit
	if offset >= len(cols) {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	end := offset + limit
	if end > len(cols) {
		end = len(cols)
	}
	writeJSON(w, http.StatusOK, cols[offset:end])
}

func (h *ContentHandler) findLetterboxdDocInDB(ctx context.Context, slug string) map[string]any {
	if h.db == nil || slug == "" {
		return nil
	}
	coll := h.db.Collection(mongodb.CollectionLetterboxd)
	filter := bson.M{"$or": []bson.M{{"_id": slug}, {"slug": slug}}}
	var doc bson.M
	if err := coll.FindOne(ctx, filter).Decode(&doc); err == nil && doc != nil {
		dSlug, _ := doc["slug"].(string)
		name, _ := doc["name"].(string)
		desc, _ := doc["description"].(string)
		banner, _ := doc["banner"].(string)
		movies := doc["movies"]
		mCount := int(getFloat(doc["movieCount"]))
		if mList, ok := movies.([]any); ok && mCount == 0 {
			mCount = len(mList)
		} else if mList2, ok2 := movies.(bson.A); ok2 && mCount == 0 {
			mCount = len(mList2)
		}
		// If movies is empty or null, check seeds to repair
		if movies == nil || mCount == 0 {
			for _, seed := range loadLetterboxdCollections() {
				sCol, _ := seed["collection"].(map[string]any)
				if sCol == nil {
					sCol = seed
				}
				sSlug, _ := seed["slug"].(string)
				if sSlug == "" {
					sSlug, _ = sCol["slug"].(string)
				}
				sName, _ := sCol["name"].(string)
				if strings.EqualFold(sSlug, slug) || strings.EqualFold(slugify(sName), slug) {
					if sMovies, ok := sCol["movies"].([]any); ok && len(sMovies) > 0 {
						movies = sMovies
						mCount = len(sMovies)
						_, _ = coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"movies": sMovies, "movieCount": len(sMovies)}})
						break
					}
				}
			}
		}
		if movies == nil || mCount == 0 {
			return nil
		}
		return map[string]any{
			"username": "letterboxd",
			"source":   "letterboxd",
			"slug":     dSlug,
			"collection": map[string]any{
				"name":        name,
				"slug":        dSlug,
				"description": desc,
				"banner":      banner,
				"movieCount":  mCount,
				"movies":      movies,
			},
		}
	}
	return nil
}

// GetLetterboxdCollection returns a single Letterboxd collection by slug or name.
// First serves from MongoDB cache (0ms). If cache miss, dynamically resolves and caches in MongoDB.
func (h *ContentHandler) GetLetterboxdCollection(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if u, err := url.PathUnescape(slug); err == nil && u != "" { slug = u } else if u, err := url.QueryUnescape(slug); err == nil && u != "" { slug = u }
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
	if u, err := url.PathUnescape(identifier); err == nil && u != "" { identifier = u } else if u, err := url.QueryUnescape(identifier); err == nil && u != "" { identifier = u }
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
			} else if mList2, ok2 := movies.(bson.A); ok2 && mCount == 0 {
				mCount = len(mList2)
			}
			// If movies is missing or empty, try seeds to repair!
			if movies == nil || mCount == 0 {
				for _, seed := range loadLetterboxdCollections() {
					sCol, _ := seed["collection"].(map[string]any)
					if sCol == nil {
						sCol = seed
					}
					sSlug, _ := seed["slug"].(string)
					if sSlug == "" {
						sSlug, _ = sCol["slug"].(string)
					}
					sName, _ := sCol["name"].(string)
					if strings.EqualFold(sSlug, target) || strings.EqualFold(sSlug, slugify(target)) ||
						strings.EqualFold(sName, target) || strings.EqualFold(slugify(sName), slugify(target)) {
						if sMovies, ok := sCol["movies"].([]any); ok && len(sMovies) > 0 {
							movies = sMovies
							mCount = len(sMovies)
							_, _ = coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"movies": sMovies, "movieCount": len(sMovies)}})
							break
						}
					}
				}
			}
			if movies != nil && mCount > 0 {
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
	if u, err := url.PathUnescape(identifier); err == nil && u != "" {
		identifier = u
	} else if u, err := url.QueryUnescape(identifier); err == nil && u != "" {
		identifier = u
	}
	target := strings.ToLower(strings.TrimSpace(identifier))
	targetSlug := slugify(target)

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
				{"_id": targetSlug},
				{"slug": target},
				{"slug": targetSlug},
				{"name": bson.M{"$regex": "^" + regexp.QuoteMeta(target) + "$", "$options": "i"}},
				{"name": bson.M{"$regex": "^" + regexp.QuoteMeta(strings.ReplaceAll(targetSlug, "-", " ")) + "$", "$options": "i"}},
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
			} else if mList2, ok2 := movies.(bson.A); ok2 && mCount == 0 {
				mCount = len(mList2)
			}
			// If movies is missing or empty, try seeds to repair!
			if movies == nil || mCount == 0 {
				for _, seed := range loadLetterboxdCollections() {
					sCol, _ := seed["collection"].(map[string]any)
					if sCol == nil {
						sCol = seed
					}
					sSlug, _ := seed["slug"].(string)
					if sSlug == "" {
						sSlug, _ = sCol["slug"].(string)
					}
					sName, _ := sCol["name"].(string)
					if strings.EqualFold(sSlug, target) || strings.EqualFold(sSlug, slugify(target)) ||
						strings.EqualFold(sName, target) || strings.EqualFold(slugify(sName), slugify(target)) {
						if sMovies, ok := sCol["movies"].([]any); ok && len(sMovies) > 0 {
							movies = sMovies
							mCount = len(sMovies)
							_, _ = coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"movies": sMovies, "movieCount": len(sMovies)}})
							break
						}
					}
				}
			}
			if movies != nil && mCount > 0 {
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
	}

	// 2. Check in-memory seeds
	cols := loadLetterboxdCollections()
	for _, entry := range cols {
		slug, _ := entry["slug"].(string)
		if strings.EqualFold(slug, target) || strings.EqualFold(slug, targetSlug) {
			return entry
		}
		if col, ok := entry["collection"].(map[string]any); ok {
			colSlug, _ := col["slug"].(string)
			if strings.EqualFold(colSlug, target) || strings.EqualFold(colSlug, targetSlug) {
				return entry
			}
			colName, _ := col["name"].(string)
			if strings.EqualFold(colName, target) || strings.EqualFold(slugify(colName), targetSlug) {
				return entry
			}
		}
	}

	return nil
}
