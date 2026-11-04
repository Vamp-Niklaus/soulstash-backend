package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

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
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	endpoint := "/3/discover/movie"
	if strings.EqualFold(params["type"], "tv") || strings.EqualFold(params["media_type"], "series") {
		endpoint = "/3/discover/tv"
	}

	// Map genre or genreId to TMDB discover parameters
	genreVal := params["genre"]
	if genreVal == "" {
		genreVal = params["genreId"]
	}
	if genreVal != "" {
		if strings.EqualFold(genreVal, "bollywood") {
			params["with_origin_country"] = "IN"
			params["with_original_language"] = "hi|ta|te|ml|kn|bn|mr|pa"
			if params["sort_by"] == "" {
				params["sort_by"] = "popularity.desc"
			}
		} else {
			params["with_genres"] = genreVal
		}
		delete(params, "genre")
		delete(params, "genreId")
	}

	data, err := h.tmdbClient.Get(r.Context(), endpoint, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch from TMDB")
		return
	}

	// Filter adult content using decision tree unless user explicitly enables showAdult
	showAdult := false
	if userClaims := middleware.GetUserFromContext(r.Context()); userClaims != nil {
		user, _ := h.userRepo.FindByID(r.Context(), userClaims.UserID)
		if user != nil {
			showAdult = user.ShowAdult
		}
	}

	if !showAdult && data != nil {
		if results, ok := data["results"].([]any); ok {
			var safe []any
			for _, res := range results {
				if rMap, ok := res.(map[string]any); ok {
					if !h.adultEngine.Evaluate(rMap) {
						safe = append(safe, res)
					}
				}
			}
			data["results"] = safe
		}
	}

	writeJSON(w, http.StatusOK, data)
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
	h.fetchProxy(w, r, "/3/movie/popular")
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
	tmdbID, _ := strconv.Atoi(r.URL.Query().Get("tmdbID"))
	mediaType := r.URL.Query().Get("mediaType")
	rating, err := h.ratingsSvc.ResolveRating(r.Context(), tmdbID, mediaType, "", nil)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rating)
}

func (h *ContentHandler) GetSingleRating(w http.ResponseWriter, r *http.Request) {
	mediaType := chi.URLParam(r, "mediaType")
	tmdbID, _ := strconv.Atoi(chi.URLParam(r, "tmdbID"))
	rating, err := h.ratingsSvc.ResolveRating(r.Context(), tmdbID, mediaType, "", nil)
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
	query := r.URL.Query().Get("query")
	if query == "" {
		query = r.URL.Query().Get("q")
	}
	if strings.TrimSpace(query) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}

	searchType := r.URL.Query().Get("type")
	if searchType == "users" {
		h.SearchUsers(w, r)
		return
	}

	pageStr := r.URL.Query().Get("page")
	if pageStr == "" {
		pageStr = "1"
	}

	endpoint := "/3/search/multi"
	if searchType == "cast" || searchType == "person" {
		endpoint = "/3/search/person"
	}

	params := map[string]string{
		"query":         query,
		"include_adult": "false",
		"page":          pageStr,
	}
	data, err := h.tmdbClient.Get(r.Context(), endpoint, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Search error")
		return
	}

	results, _ := data["results"].([]any)
	yearQuery, _ := strconv.Atoi(r.URL.Query().Get("year"))

	if searchType == "cast" || searchType == "person" {
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
		return
	}

	type scoredItem struct {
		item  map[string]any
		score float64
	}

	var scored []scoredItem
	for _, res := range results {
		if rMap, ok := res.(map[string]any); ok {
			mediaType, _ := rMap["media_type"].(string)
			if mediaType == "movie" || mediaType == "tv" {
				normalized := service.NormalizeMediaItem(rMap, mediaType)
				s := service.ComputeFinalScore(normalized, query, yearQuery)
				scored = append(scored, scoredItem{item: normalized, score: s})
			} else {
				scored = append(scored, scoredItem{item: rMap, score: 0})
			}
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	finalResults := make([]map[string]any, len(scored))
	for i, s := range scored {
		finalResults[i] = s.item
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": finalResults})
}

func (h *ContentHandler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	users, err := h.userRepo.FindAllUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to search users")
		return
	}

	var matches []map[string]any
	lower := strings.ToLower(query)
	for _, u := range users {
		if strings.Contains(strings.ToLower(u.Username), lower) || strings.Contains(strings.ToLower(u.FullName), lower) {
			matches = append(matches, map[string]any{
				"id":       u.ID.Hex(),
				"username": u.Username,
				"fullName": u.FullName,
				"avatar":   u.Avatar,
			})
		}
	}

	writeJSON(w, http.StatusOK, matches)
}

func (h *ContentHandler) GetPlayerSources(w http.ResponseWriter, r *http.Request) {
	// Return empty or player sources structure
	writeJSON(w, http.StatusOK, map[string]any{
		"sources":   map[string]any{},
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
