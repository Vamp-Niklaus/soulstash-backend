package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"soulstash/internal/middleware"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/repository"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
)

type ContentHandler struct {
	tmdbClient  tmdb.Client
	ratingsSvc  service.RatingsService
	adultEngine *service.AdultDecisionEngine
	userRepo    repository.UserRepository
	jwtSecret   string
}

func NewContentHandler(
	tmdbClient tmdb.Client,
	ratingsSvc service.RatingsService,
	adultEngine *service.AdultDecisionEngine,
	userRepo repository.UserRepository,
	jwtSecret string,
) *ContentHandler {
	return &ContentHandler{
		tmdbClient:  tmdbClient,
		ratingsSvc:  ratingsSvc,
		adultEngine: adultEngine,
		userRepo:    userRepo,
		jwtSecret:   jwtSecret,
	}
}

func (h *ContentHandler) RegisterRoutes(r chi.Router) {
	r.Group(func(cr chi.Router) {
		// OptionalAuth on all content routes
		cr.Use(middleware.OptionalAuth(h.jwtSecret))

		// Similar titles
		cr.Get("/movies/{id}/similar", h.GetSimilarMovies)
		cr.Get("/series/{id}/similar", h.GetSimilarSeries)

		// TMDB collections & person images
		cr.Get("/tmdb-collection/{id}", h.GetTMDBCollection)
		cr.Get("/person/{id}/images", h.GetPersonImages)
		cr.Get("/people/{id}/images", h.GetPersonImages)
		cr.Get("/imdb/person/{personId}/filmography", h.GetPersonFilmography)

		// Discovery & Details
		cr.Get("/movies", h.DiscoverMovies)
		cr.Get("/movies/{id}", h.GetMovieDetail)
		cr.Get("/movie/{id}", h.GetMovieDetail)
		cr.Get("/movies/{id}/images", h.GetMovieImages)
		cr.Get("/movie/{id}/images", h.GetMovieImages)
		cr.Get("/series/{id}/images", h.GetSeriesImages)
		cr.Get("/tv/{id}/images", h.GetSeriesImages)
		cr.Get("/movies/{id}/credits", h.GetMovieCredits)
		cr.Get("/movie/{id}/credits", h.GetMovieCredits)
		cr.Get("/series/{id}/credits", h.GetSeriesCredits)
		cr.Get("/series/{id}", h.GetSeriesDetail)
		cr.Get("/series/{id}/season/{seasonNumber}", h.GetSeriesSeason)

		// Person Details & Credits (both singular & plural aliases)
		cr.Get("/person/{id}", h.GetPersonDetail)
		cr.Get("/people/{id}", h.GetPersonDetail)
		cr.Get("/person/{id}/credits", h.GetPersonCredits)
		cr.Get("/people/{id}/credits", h.GetPersonCredits)

		// Feeds
		cr.Get("/trending", h.GetTrending)
		cr.Get("/popular", h.GetPopular)
		cr.Get("/genres", h.GetGenres)
		cr.Get("/home", h.GetHome)

		// Ratings
		cr.Get("/ratings", h.QueryRatings)
		cr.Get("/ratings/{mediaType}/{tmdbID}", h.GetSingleRating)
		cr.Get("/ratings/stats/counters", h.GetRatingCounters)
		cr.Post("/ratings/imdb/enrich", h.BatchEnrichImdb)

		// Search
		cr.Get("/search", h.Search)
		cr.Get("/search/users", h.SearchUsers)

		// Player sources & embed
		cr.Get("/player/sources", h.GetPlayerSources)
		cr.Get("/player/fallback", h.GetPlayerFallback)
		cr.Get("/scrape-embed", h.ScrapeEmbed)
	})
}

func (h *ContentHandler) GetSimilarMovies(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/movie/%s/similar", id))
}

func (h *ContentHandler) GetSimilarSeries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/similar", id))
}

func (h *ContentHandler) GetTMDBCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/collection/%s", id))
}

func (h *ContentHandler) GetPersonImages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/person/%s/images", id))
}

func (h *ContentHandler) GetPersonFilmography(w http.ResponseWriter, r *http.Request) {
	// Return empty list or filmography if scraped
	writeJSON(w, http.StatusOK, []any{})
}

func (h *ContentHandler) DiscoverMovies(w http.ResponseWriter, r *http.Request) {
	genreVal := r.URL.Query().Get("genre")
	if genreVal == "" {
		genreVal = r.URL.Query().Get("genreId")
	}
	if strings.EqualFold(genreVal, "bollywood") {
		h.discoverBollywood(w, r)
		return
	}
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	targetCount := limit
	if page == 1 && targetCount < 14 {
		targetCount = 14
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	year := strings.TrimSpace(r.URL.Query().Get("year"))
	sortBy := strings.ToLower(r.URL.Query().Get("sortBy"))
	if sortBy == "" {
		sortBy = "popularity"
	}
	if sortBy != "release_date" && sortBy != "vote_average" {
		sortBy = "popularity"
	}
	sortOrder := strings.ToLower(r.URL.Query().Get("sortOrder"))
	if sortOrder != "asc" {
		sortOrder = "desc"
	}
	adminMode := h.searchAdminMode(r)
	includeAdult := strconv.FormatBool(adminMode != 0)
	items := make([]map[string]any, 0, targetCount)
	totalPages, totalResults := 1, 0
	maxPage := page + 6
	if search != "" {
		maxPage = page
	}
	for currentPage := page; len(items) < targetCount && currentPage <= maxPage; currentPage++ {
		movieParams := map[string]string{"language": "en-US", "page": strconv.Itoa(currentPage), "include_adult": includeAdult}
		tvParams := cloneStringMap(movieParams)
		movieEndpoint, tvEndpoint := "/3/discover/movie", "/3/discover/tv"
		if search != "" {
			movieEndpoint, tvEndpoint = "/3/search/movie", "/3/search/tv"
			movieParams["query"], tvParams["query"] = search, search
		} else {
			movieParams["sort_by"], tvParams["sort_by"] = sortBy+"."+sortOrder, sortBy+"."+sortOrder
			if adminMode != 2 {
				movieParams["vote_count.gte"], tvParams["vote_count.gte"] = "5", "5"
			}
			if genreVal != "" {
				movieGenre, tvGenre := mapGenreForMovie(genreVal), mapGenreForTV(genreVal)
				if movieGenre == "" {
					movieGenre = genreVal
				}
				if tvGenre == "" {
					tvGenre = genreVal
				}
				movieParams["with_genres"], tvParams["with_genres"] = movieGenre, tvGenre
			}
			if year != "" {
				movieParams["primary_release_year"], tvParams["first_air_date_year"] = year, year
			}
		}
		movieData, movieErr := h.tmdbClient.Get(r.Context(), movieEndpoint, movieParams)
		tvData, tvErr := h.tmdbClient.Get(r.Context(), tvEndpoint, tvParams)
		if movieErr != nil && tvErr != nil {
			writeError(w, http.StatusInternalServerError, "Failed to fetch from TMDB")
			return
		}
		if movieErr == nil {
			items = append(items, discoverItems(movieData, "Movie", adminMode, h.adultEngine, false)...)
			totalPages = max(totalPages, int(getFloat(movieData["total_pages"])))
			totalResults += int(getFloat(movieData["total_results"]))
		}
		if tvErr == nil {
			items = append(items, discoverItems(tvData, "Series", adminMode, h.adultEngine, false)...)
			totalPages = max(totalPages, int(getFloat(tvData["total_pages"])))
			totalResults += int(getFloat(tvData["total_results"]))
		}
		items = uniquePopularItems(items)
	}
	if genreVal != "16" {
		items = withoutAnimeItems(items)
	}
	if len(items) > targetCount {
		items = items[:targetCount]
	}
	pages := max(totalPages, 500)
	totalResults = max(totalResults, pages*limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"movies":     items,
		"pagination": map[string]int{"page": page, "limit": limit, "total": totalResults, "pages": pages},
	})
}

func (h *ContentHandler) discoverBollywood(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	targetCount := limit
	if page == 1 && targetCount < 14 {
		targetCount = 14
	}
	adminMode := h.searchAdminMode(r)
	currentWindow := (page - 1) / 3
	currentTmdbPage := (page-1)%3 + 1
	items := make([]map[string]any, 0, targetCount)
	for attempts := 0; len(items) < targetCount && attempts < 8; attempts++ {
		startDate, endDate := fourMonthWindow(currentWindow)
		common := map[string]string{
			"language": "en-US", "page": strconv.Itoa(currentTmdbPage),
			"include_adult":       strconv.FormatBool(adminMode != 0),
			"with_origin_country": "IN", "with_original_language": "hi|ta|te|ml|kn|bn|mr|pa",
			"sort_by": "popularity.desc",
		}
		movieParams, tvParams := cloneStringMap(common), cloneStringMap(common)
		movieParams["primary_release_date.gte"], movieParams["primary_release_date.lte"] = startDate, endDate
		tvParams["first_air_date.gte"], tvParams["first_air_date.lte"] = startDate, endDate
		movieData, movieErr := h.tmdbClient.Get(r.Context(), "/3/discover/movie", movieParams)
		tvData, tvErr := h.tmdbClient.Get(r.Context(), "/3/discover/tv", tvParams)
		if movieErr == nil {
			items = append(items, discoverItems(movieData, "Movie", adminMode, h.adultEngine, true)...)
		}
		if tvErr == nil {
			items = append(items, discoverItems(tvData, "Series", adminMode, h.adultEngine, true)...)
		}
		items = uniquePopularItems(items)
		items = withoutAnimeItems(items)
		totalPages := max(int(getFloat(movieData["total_pages"])), int(getFloat(tvData["total_pages"])))
		if currentTmdbPage < min(max(1, totalPages), 3) {
			currentTmdbPage++
		} else {
			currentWindow++
			currentTmdbPage = 1
		}
	}
	if len(items) > targetCount {
		items = items[:targetCount]
	}
	pages := max(500, page+20)
	writeJSON(w, http.StatusOK, map[string]any{
		"movies":     items,
		"pagination": map[string]int{"page": page, "limit": limit, "total": pages * limit, "pages": pages},
	})
}

func fourMonthWindow(window int) (string, string) {
	now := time.Now().UTC()
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := date.AddDate(0, -(window+1)*4, 0)
	end := date.AddDate(0, -window*4, 0)
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}

func cloneStringMap(input map[string]string) map[string]string {
	clone := make(map[string]string, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}

func mapGenreForTV(genre string) string {
	mapping := map[string]string{
		"28": "10759", "12": "10759", "16": "16", "35": "35", "80": "80", "99": "99", "18": "18",
		"10751": "10751", "14": "10765", "36": "10768", "9648": "9648", "878": "10765", "10752": "10768",
		"37": "37", "10759": "10759", "10762": "10762", "10763": "10763", "10764": "10764", "10765": "10765",
		"10766": "10766", "10767": "10767", "10768": "10768",
	}
	return mapping[genre]
}

func mapGenreForMovie(genre string) string {
	mapping := map[string]string{
		"28": "28", "12": "12", "16": "16", "35": "35", "80": "80", "99": "99", "18": "18",
		"10751": "10751", "14": "14", "36": "36", "27": "27", "10402": "10402", "9648": "9648",
		"10749": "10749", "878": "878", "10770": "10770", "53": "53", "10752": "10752", "37": "37",
		"10759": "28|12", "10765": "878|14", "10768": "10752|36", "10762": "10751", "10764": "99",
	}
	return mapping[genre]
}

func discoverItems(data map[string]any, mediaType string, adminMode int, adultEngine *service.AdultDecisionEngine, bollywood bool) []map[string]any {
	results, _ := data["results"].([]any)
	items := make([]map[string]any, 0, len(results))
	for _, raw := range results {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		posterPath, _ := item["poster_path"].(string)
		if strings.TrimSpace(posterPath) == "" {
			continue
		}
		if bollywood && adminMode == 2 {
			item["adult"] = true
			item["_isAdultDiscover"] = true
		}
		if !searchItemVisible(item, adminMode, adultEngine) {
			continue
		}
		item["media_type"] = mediaType
		if bollywood {
			item["is_bollywood_endpoint"] = true
		}
		items = append(items, item)
	}
	return items
}

func withoutAnimeItems(items []map[string]any) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !isAnimeDiscoverItem(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func isAnimeDiscoverItem(item map[string]any) bool {
	language, _ := item["original_language"].(string)
	if language != "ja" && language != "zh" && language != "ko" {
		return false
	}
	hasAnimation := false
	genres, _ := item["genre_ids"].([]any)
	for _, genre := range genres {
		if getFloat(genre) == 16 {
			hasAnimation = true
			break
		}
	}
	if !hasAnimation {
		if namedGenres, ok := item["genres"].([]any); ok {
			for _, genre := range namedGenres {
				if genreMap, ok := genre.(map[string]any); ok && genreMap["name"] == "Animation" {
					hasAnimation = true
					break
				}
				if genre == "Animation" {
					hasAnimation = true
					break
				}
			}
		}
	}
	if !hasAnimation {
		return false
	}
	countries, _ := item["origin_country"].([]any)
	for _, country := range countries {
		if country == "JP" || country == "CN" || country == "KR" {
			return true
		}
	}
	// Movie records from TMDB use production_countries, while TV records use
	// origin_country. Node's anime filter checks both shapes.
	if productionCountries, ok := item["production_countries"].([]any); ok {
		for _, country := range productionCountries {
			countryMap, ok := country.(map[string]any)
			if !ok {
				continue
			}
			code, _ := countryMap["iso_3166_1"].(string)
			if code == "JP" || code == "CN" || code == "KR" {
				return true
			}
		}
	}
	return false
}

func uniquePopularItems(items []map[string]any) []map[string]any {
	seen := make(map[string]struct{}, len(items))
	unique := make([]map[string]any, 0, len(items))
	for _, item := range items {
		key := fmt.Sprintf("%v:%v", item["media_type"], item["id"])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, item)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return getFloat(unique[i]["popularity"]) > getFloat(unique[j]["popularity"])
	})
	return unique
}

func (h *ContentHandler) GetMovieDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	params := map[string]string{"append_to_response": "videos,credits,recommendations,images", "include_image_language": "en,null"}
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/movie/%s", id), params)
	if err != nil {
		writeError(w, http.StatusNotFound, "Movie not found")
		return
	}

	// Attach resolved IMDb rating
	tmdbID, _ := strconv.Atoi(id)
	imdbID, _ := data["imdb_id"].(string)
	voteAvg := getFloat(data["vote_average"])
	rating, _ := h.ratingsSvc.ResolveRating(r.Context(), tmdbID, "Movie", imdbID, &voteAvg)
	if rating != nil {
		data["imdb_rating"] = rating.ImdbRating
	}

	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) GetMovieCredits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/movie/%s/credits", id))
}

func (h *ContentHandler) GetMovieImages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/movie/%s/images?include_image_language=en,null", id))
}

func (h *ContentHandler) GetSeriesImages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/images?include_image_language=en,null", id))
}

func (h *ContentHandler) GetSeriesCredits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/credits", id))
}

func (h *ContentHandler) GetSeriesDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	params := map[string]string{"append_to_response": "videos,credits,recommendations,external_ids,images", "include_image_language": "en,null"}
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/tv/%s", id), params)
	if err != nil {
		writeError(w, http.StatusNotFound, "Series not found")
		return
	}

	tmdbID, _ := strconv.Atoi(id)
	var imdbID string
	if ext, ok := data["external_ids"].(map[string]any); ok {
		imdbID, _ = ext["imdb_id"].(string)
	}
	voteAvg := getFloat(data["vote_average"])
	rating, _ := h.ratingsSvc.ResolveRating(r.Context(), tmdbID, "Series", imdbID, &voteAvg)
	if rating != nil {
		data["imdb_rating"] = rating.ImdbRating
	}

	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) GetSeriesSeason(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	season := chi.URLParam(r, "seasonNumber")
	h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/season/%s", id, season))
}

func (h *ContentHandler) GetPersonDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	params := map[string]string{"append_to_response": "combined_credits,images"}
	h.fetchProxyWithParams(w, r, fmt.Sprintf("/3/person/%s", id), params)
}

func (h *ContentHandler) GetPersonCredits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/person/%s/combined_credits", id))
}

func (h *ContentHandler) GetTrending(w http.ResponseWriter, r *http.Request) {
	timeWindow := r.URL.Query().Get("time_window")
	if timeWindow == "" {
		timeWindow = "day"
	}
	h.fetchProxy(w, r, fmt.Sprintf("/3/trending/all/%s", timeWindow))
}

func (h *ContentHandler) GetPopular(w http.ResponseWriter, r *http.Request) {
	mediaType := r.URL.Query().Get("type")
	if mediaType == "tv" || mediaType == "series" {
		h.fetchProxy(w, r, "/3/tv/popular")
		return
	}

	// Keep the response shape and filters aligned with the Node /popular route,
	// while combining the movie and TV discovery feeds into one popular list.
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	targetCount := limit
	if page == 1 && targetCount < 14 {
		targetCount = 14
	}
	adminMode := h.searchAdminMode(r)
	today := time.Now().UTC().Format("2006-01-02")
	items := make([]map[string]any, 0, targetCount)
	moviePages, seriesPages := 1, 1
	movieTotal, seriesTotal := 0, 0
	currentPage := page

	for attempts := 0; len(items) < targetCount && attempts < 5; attempts++ {
		movieParams := map[string]string{
			"language":                "en-US",
			"page":                    strconv.Itoa(currentPage),
			"include_adult":           strconv.FormatBool(adminMode == 2),
			"sort_by":                 "popularity.desc",
			"primary_release_date.lte": today,
		}
		seriesParams := cloneStringMap(movieParams)
		delete(seriesParams, "primary_release_date.lte")
		seriesParams["first_air_date.lte"] = today

		var movieData, seriesData map[string]any
		var movieErr, seriesErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			movieData, movieErr = h.tmdbClient.Get(r.Context(), "/3/discover/movie", movieParams)
		}()
		go func() {
			defer wg.Done()
			seriesData, seriesErr = h.tmdbClient.Get(r.Context(), "/3/discover/tv", seriesParams)
		}()
		wg.Wait()

		if movieErr != nil && seriesErr != nil {
			if len(items) == 0 {
				writeError(w, http.StatusBadGateway, "Failed to fetch popular content")
				return
			}
			break
		}

		markAdultDiscoverResults := func(data map[string]any) {
			if adminMode != 2 || data == nil {
				return
			}
			results, _ := data["results"].([]any)
			for _, raw := range results {
				if item, ok := raw.(map[string]any); ok {
					item["adult"] = true
					item["_isAdultDiscover"] = true
				}
			}
		}
		markAdultDiscoverResults(movieData)
		markAdultDiscoverResults(seriesData)

		pageItems := make([]map[string]any, 0, targetCount)
		if movieErr == nil {
			pageItems = append(pageItems, discoverItems(movieData, "Movie", adminMode, h.adultEngine, false)...)
			moviePages = max(moviePages, int(getFloat(movieData["total_pages"])))
			movieTotal = max(movieTotal, int(getFloat(movieData["total_results"])))
		}
		if seriesErr == nil {
			pageItems = append(pageItems, discoverItems(seriesData, "Series", adminMode, h.adultEngine, false)...)
			seriesPages = max(seriesPages, int(getFloat(seriesData["total_pages"])))
			seriesTotal = max(seriesTotal, int(getFloat(seriesData["total_results"])))
		}
		pageItems = withoutAnimeItems(pageItems)
		items = uniquePopularItems(append(items, pageItems...))

		movieResults, _ := movieData["results"].([]any)
		seriesResults, _ := seriesData["results"].([]any)
		if (currentPage >= max(moviePages, seriesPages)) || (len(movieResults) == 0 && len(seriesResults) == 0) {
			break
		}
		currentPage++
	}

	if len(items) > targetCount {
		items = items[:targetCount]
	}
	totalPages := max(moviePages, seriesPages)
	totalResults := movieTotal + seriesTotal
	safeTotalPages := totalPages
	if safeTotalPages <= 1 {
		safeTotalPages = 500
	}
	safeTotalPages = max(safeTotalPages, page+10)
	totalResults = max(totalResults, safeTotalPages*limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"movies": items,
		"results": items,
		"page": page,
		"total_pages": safeTotalPages,
		"total_results": totalResults,
		"pagination": map[string]int{
			"page": page, "limit": limit, "total": totalResults, "pages": safeTotalPages,
		},
	})
}

func (h *ContentHandler) GetGenres(w http.ResponseWriter, r *http.Request) {
	// Returns combined movie and tv genres
	movieData, _ := h.tmdbClient.Get(r.Context(), "/3/genre/movie/list", nil)
	tvData, _ := h.tmdbClient.Get(r.Context(), "/3/genre/tv/list", nil)

	seen := make(map[float64]bool)
	var combined []any

	// Prepend Bollywood virtual genre (like React SPA)
	combined = append(combined, map[string]any{
		"id":   "bollywood",
		"name": "Bollywood",
	})

	if movieData != nil {
		if genres, ok := movieData["genres"].([]any); ok {
			for _, g := range genres {
				if gMap, ok := g.(map[string]any); ok {
					id := getFloat(gMap["id"])
					seen[id] = true
					combined = append(combined, g)
				}
			}
		}
	}
	if tvData != nil {
		if genres, ok := tvData["genres"].([]any); ok {
			for _, g := range genres {
				if gMap, ok := g.(map[string]any); ok {
					id := getFloat(gMap["id"])
					if !seen[id] {
						seen[id] = true
						combined = append(combined, g)
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"genres": combined})
}

func (h *ContentHandler) GetHome(w http.ResponseWriter, r *http.Request) {
	trending, _ := h.tmdbClient.Get(r.Context(), "/3/trending/all/day", nil)
	popularMovies, _ := h.tmdbClient.Get(r.Context(), "/3/movie/popular", nil)
	popularSeries, _ := h.tmdbClient.Get(r.Context(), "/3/tv/popular", nil)

	writeJSON(w, http.StatusOK, map[string]any{
		"trending":      trending,
		"popularMovies": popularMovies,
		"popularSeries": popularSeries,
	})
}

func (h *ContentHandler) QueryRatings(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit == 0 {
		limit = 5000
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 10000 {
		limit = 10000
	}
	items, err := h.ratingsSvc.ListRatings(r.Context(), r.URL.Query().Get("mediaType"), limit)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *ContentHandler) GetSingleRating(w http.ResponseWriter, r *http.Request) {
	mediaType := chi.URLParam(r, "mediaType")
	tmdbID, _ := strconv.Atoi(chi.URLParam(r, "tmdbID"))
	rating, err := h.ratingsSvc.GetStoredRating(r.Context(), tmdbID, mediaType)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rating)
}

func (h *ContentHandler) GetRatingCounters(w http.ResponseWriter, r *http.Request) {
	counters, err := h.ratingsSvc.GetCounters(r.Context())
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counters)
}

func (h *ContentHandler) BatchEnrichImdb(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Items required")
		return
	}

	enriched := h.ratingsSvc.BatchEnrich(r.Context(), body.Items)
	writeJSON(w, http.StatusOK, map[string]any{"items": enriched})
}

func (h *ContentHandler) Search(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Surrogate-Control", "no-store")
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	searchType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}, "page": page, "hasMore": false})
		return
	}
	if searchType == "users" {
		h.searchUsersResponse(w, r, query, limit, page)
		return
	}
	if searchType == "person" {
		searchType = "cast"
	}

	adminMode := h.searchAdminMode(r)
	baseQuery := query
	alteredQuery := query
	yearQuery := 0
	var yearPattern = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	if yearMatch := yearPattern.FindString(query); yearMatch != "" {
		yearQuery, _ = strconv.Atoi(yearMatch)
		alteredQuery = strings.TrimSpace(strings.Replace(query, yearMatch, "", 1))
	}

	type pageResult struct {
		mediaType string
		page      int
		data      map[string]any
	}
	startPage := (page-1)*2 + 1
	endPage := startPage + 1
	queries := []struct {
		mediaType string
		query     string
		page      int
		year      string
	}{}
	if searchType == "" || searchType == "content" || searchType == "all" {
		for tmdbPage := startPage; tmdbPage <= endPage; tmdbPage++ {
			queries = append(queries,
				struct {
					mediaType string
					query     string
					page      int
					year      string
				}{"movie", baseQuery, tmdbPage, ""},
				struct {
					mediaType string
					query     string
					page      int
					year      string
				}{"tv", baseQuery, tmdbPage, ""},
			)
		}
		if yearQuery > 0 && len([]rune(alteredQuery)) >= 2 && page == 1 {
			for _, mediaType := range []string{"movie", "tv"} {
				queries = append(queries, struct {
					mediaType string
					query     string
					page      int
					year      string
				}{mediaType, alteredQuery, 1, strconv.Itoa(yearQuery)})
			}
		}
	}
	if searchType == "cast" || searchType == "all" {
		for tmdbPage := startPage; tmdbPage <= endPage; tmdbPage++ {
			queries = append(queries, struct {
				mediaType string
				query     string
				page      int
				year      string
			}{"person", baseQuery, tmdbPage, ""})
		}
	}

	resultsCh := make(chan pageResult, len(queries))
	var wg sync.WaitGroup
	includeAdult := adminMode != 0
	for _, queryItem := range queries {
		queryItem := queryItem
		wg.Add(1)
		go func() {
			defer wg.Done()
			params := map[string]string{
				"query":         queryItem.query,
				"include_adult": strconv.FormatBool(includeAdult || queryItem.mediaType == "person"),
				"language":      "en-US",
				"page":          strconv.Itoa(queryItem.page),
			}
			if queryItem.year != "" {
				params["year"] = queryItem.year
			}
			data, err := h.tmdbClient.Get(r.Context(), "/3/search/"+queryItem.mediaType, params)
			if err != nil {
				data = map[string]any{}
			}
			resultsCh <- pageResult{mediaType: queryItem.mediaType, page: queryItem.page, data: data}
		}()
	}
	wg.Wait()
	close(resultsCh)

	var movies, series, people []map[string]any
	maxPages := 1
	for res := range resultsCh {
		if pages := int(getFloat(res.data["total_pages"])); pages > maxPages {
			maxPages = pages
		}
		items, _ := res.data["results"].([]any)
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok || item["id"] == nil {
				continue
			}
			switch res.mediaType {
			case "movie":
				item["media_type"] = "movie"
				movies = append(movies, item)
			case "tv":
				item["media_type"] = "tv"
				series = append(series, item)
			case "person":
				people = append(people, item)
			}
		}
	}

	finalResults := make([]map[string]any, 0, limit)
	personResults := shapeSearchPeople(people, adminMode, h.adultEngine)
	if searchType == "cast" {
		finalResults = personResults[:min(limit, len(personResults))]
	} else if searchType == "" || searchType == "content" || searchType == "all" {
		type scoredItem struct {
			item  map[string]any
			score float64
		}
		scored := make([]scoredItem, 0, len(movies)+len(series))
		for _, raw := range append(movies, series...) {
			if !searchItemVisible(raw, adminMode, h.adultEngine) {
				continue
			}
			mediaType, _ := raw["media_type"].(string)
			item := service.NormalizeMediaItem(raw, mediaType)
			scoreQuery := query
			if yearQuery > 0 {
				scoreQuery = alteredQuery
			}
			rawScore := service.ComputeFinalScore(item, scoreQuery, yearQuery)
			title, _ := item["title"].(string)
			originalTitle, _ := item["originalTitle"].(string)
			qLower := strings.ToLower(strings.TrimSpace(query))
			if !strings.HasPrefix(strings.ToLower(title), qLower) && !strings.HasPrefix(strings.ToLower(originalTitle), qLower) && rawScore <= 25 {
				continue
			}
			item["batch"] = page
			item["score"] = rawScore - float64(page-1)*1000
			item["isIndian"] = searchItemIsIndian(item)
			scored = append(scored, scoredItem{item: item, score: rawScore - float64(page-1)*1000})
		}
		sort.SliceStable(scored, func(i, j int) bool {
			if yearQuery > 0 {
				iYear := int(getFloat(scored[i].item["release_year"])) == yearQuery
				jYear := int(getFloat(scored[j].item["release_year"])) == yearQuery
				if iYear != jYear {
					return iYear
				}
			}
			return scored[i].score > scored[j].score
		})
		primaryLimit := limit
		if searchType == "all" {
			primaryLimit = max(0, limit-min(4, len(personResults)))
		}
		for _, item := range scored {
			if len(finalResults) >= primaryLimit {
				break
			}
			finalResults = append(finalResults, item.item)
		}
		if searchType == "all" {
			for i := 0; i < min(4, len(personResults)) && len(finalResults) < limit; i++ {
				finalResults = append(finalResults, personResults[i])
			}
		}
	}

	hasMore := endPage < maxPages
	response := map[string]any{"query": query, "results": finalResults, "page": page, "hasMore": hasMore}
	if r.URL.Query().Get("stream") == "1" {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)
		encoder := json.NewEncoder(w)
		_ = encoder.Encode(map[string]any{"type": "start", "query": query, "page": page})
		if flusher != nil {
			flusher.Flush()
		}
		if len(finalResults) > 0 {
			_ = encoder.Encode(map[string]any{"type": "results", "query": query, "page": page, "source": searchType, "results": finalResults})
			if flusher != nil {
				flusher.Flush()
			}
		}
		_ = encoder.Encode(map[string]any{"type": "done", "query": query, "page": page, "hasMore": hasMore})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *ContentHandler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	h.searchUsersResponse(w, r, query, parsePositiveInt(r.URL.Query().Get("limit"), 12), 1)
}

func (h *ContentHandler) searchUsersResponse(w http.ResponseWriter, r *http.Request, query string, limit, page int) {
	if len([]rune(query)) < 2 || h.userRepo == nil {
		writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": []any{}, "page": page, "hasMore": false})
		return
	}
	users, err := h.userRepo.FindAllUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to search users")
		return
	}
	lower := strings.ToLower(query)
	matches := make([]map[string]any, 0)
	for _, user := range users {
		if strings.Contains(strings.ToLower(user.Username), lower) || strings.Contains(strings.ToLower(user.FullName), lower) {
			id := user.ID.Hex()
			name := user.FullName
			if name == "" {
				name = user.Username
			}
			matches = append(matches, map[string]any{
				"id": id, "_id": id, "media_type": "User", "title": user.Username,
				"name": name, "username": user.Username, "poster_path": nullString(user.Avatar),
				"profile_path": nullString(user.Avatar), "fullName": user.FullName, "bio": user.Bio,
			})
			if len(matches) >= limit {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": matches, "page": page, "hasMore": false})
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func (h *ContentHandler) searchAdminMode(r *http.Request) int {
	claims := middleware.GetUserFromContext(r.Context())
	if claims == nil || h.userRepo == nil {
		return 0
	}
	user, err := h.userRepo.FindByID(r.Context(), claims.UserID)
	if err != nil || user == nil || !user.Admin {
		return 0
	}
	mode := user.AdminMode
	if mode == 0 && user.ShowAdult {
		mode = 1
	}
	if mode < 0 || mode > 2 {
		return 0
	}
	return mode
}

func searchItemVisible(item map[string]any, adminMode int, adultEngine *service.AdultDecisionEngine) bool {
	if adminMode == 1 {
		return true
	}
	adult := item["adult"] == true
	if adultEngine != nil {
		adult = adultEngine.Evaluate(item)
	}
	if adminMode == 2 {
		return adult
	}
	return !adult
}

func searchItemIsIndian(item map[string]any) bool {
	lang, _ := item["originalLanguage"].(string)
	if service.IndianLanguages[lang] {
		return true
	}
	countries, _ := item["originCountry"].([]any)
	for _, country := range countries {
		if country == "IN" {
			return true
		}
	}
	return false
}

func shapeSearchPeople(people []map[string]any, adminMode int, adultEngine *service.AdultDecisionEngine) []map[string]any {
	results := make([]map[string]any, 0, len(people))
	for _, person := range people {
		knownFor, _ := person["known_for"].([]any)
		filteredKnownFor := make([]any, 0, len(knownFor))
		isIndian := false
		for _, raw := range knownFor {
			item, ok := raw.(map[string]any)
			if !ok || !searchItemVisible(item, adminMode, adultEngine) {
				continue
			}
			filteredKnownFor = append(filteredKnownFor, item)
			if searchItemIsIndian(item) {
				isIndian = true
			}
		}
		name, _ := person["name"].(string)
		profilePath, _ := person["profile_path"].(string)
		poster := any(nil)
		if profilePath != "" {
			poster = profilePath
		}
		department, _ := person["known_for_department"].(string)
		if department == "" {
			department = "Cast & Crew"
		}
		results = append(results, map[string]any{
			"id": person["id"], "media_type": "Person", "title": name, "name": name,
			"gender": person["gender"], "adult": person["adult"] == true, "poster_path": poster,
			"profile_path": poster, "known_for_department": department, "known_for": filteredKnownFor,
			"_isIndian": isIndian, "_popularity": getFloat(person["popularity"]),
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i]["_isIndian"] != results[j]["_isIndian"] {
			return results[i]["_isIndian"] == true
		}
		iHasPhoto := results[i]["profile_path"] != nil
		jHasPhoto := results[j]["profile_path"] != nil
		if iHasPhoto != jHasPhoto {
			return iHasPhoto
		}
		return getFloat(results[i]["_popularity"]) > getFloat(results[j]["_popularity"])
	})
	for _, result := range results {
		delete(result, "_isIndian")
		delete(result, "_popularity")
	}
	return results
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (h *ContentHandler) GetPlayerSources(w http.ResponseWriter, r *http.Request) {
	tmdbID := r.URL.Query().Get("tmdbId")
	mediaType := r.URL.Query().Get("mediaType")

	var sources []map[string]any

	if tmdbID != "" {
		isSeries := strings.EqualFold(mediaType, "series") || strings.EqualFold(mediaType, "tv")
		endpoints := []string{}
		if isSeries {
			season := parsePositiveInt(r.URL.Query().Get("seasonNumber"), 0)
			episode := parsePositiveInt(r.URL.Query().Get("episodeNumber"), 0)
			if season > 0 && episode > 0 {
				endpoints = append(endpoints, fmt.Sprintf("/3/tv/%s/season/%d/episode/%d/videos", tmdbID, season, episode))
				if season != 1 || episode != 1 {
					endpoints = append(endpoints, fmt.Sprintf("/3/tv/%s/season/1/episode/1/videos", tmdbID))
				}
			}
			endpoints = append(endpoints, fmt.Sprintf("/3/tv/%s/videos", tmdbID))
		} else {
			endpoints = append(endpoints, fmt.Sprintf("/3/movie/%s/videos", tmdbID))
		}
		var trailerKey string
		for _, endpoint := range endpoints {
			raw, err := h.tmdbClient.GetRaw(r.Context(), endpoint, nil)
			if err != nil {
				continue
			}
			var vidResp struct {
				Results []struct {
					Key  string `json:"key"`
					Site string `json:"site"`
					Type string `json:"type"`
				} `json:"results"`
			}
			if json.Unmarshal(raw, &vidResp) != nil {
				continue
			}
			for _, preferredType := range []string{"Trailer", "Teaser", "Clip", ""} {
				for _, video := range vidResp.Results {
					if video.Site == "YouTube" && video.Key != "" && (preferredType == "" || video.Type == preferredType) {
						trailerKey = video.Key
						break
					}
				}
				if trailerKey != "" {
					break
				}
			}
			if trailerKey != "" {
				break
			}
		}
		if trailerKey != "" {
			ytURL := fmt.Sprintf("https://www.youtube-nocookie.com/embed/%s", trailerKey)
			sources = append(sources, map[string]any{
				"id": "youtube", "key": "youtube", "label": "YouTube",
				"url": ytURL, "urls": []string{ytURL}, "embeddable": true,
			})
		}
	}

	if sources == nil {
		sources = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sources":   sources,
		"downloads": []any{},
	})
}

func (h *ContentHandler) GetPlayerFallback(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"sources": []any{},
	})
}

func (h *ContentHandler) ScrapeEmbed(w http.ResponseWriter, r *http.Request) {
	urlParam := r.URL.Query().Get("url")
	writeJSON(w, http.StatusOK, map[string]any{
		"url": urlParam,
	})
}

func (h *ContentHandler) fetchProxy(w http.ResponseWriter, r *http.Request, endpoint string) {
	h.fetchProxyWithParams(w, r, endpoint, nil)
}

func (h *ContentHandler) fetchProxyWithParams(w http.ResponseWriter, r *http.Request, endpoint string, extraParams map[string]string) {
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	for k, v := range extraParams {
		params[k] = v
	}

	data, err := h.tmdbClient.GetRaw(r.Context(), endpoint, params)
	if err != nil {
		writeError(w, http.StatusNotFound, "Content not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func getFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}
