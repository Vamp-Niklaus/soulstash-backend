package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"soulstash/internal/middleware"
	"soulstash/internal/platform/mongodb"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/repository"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type homeCacheEntry struct {
	data      map[string]any
	expiresAt time.Time
}

type ContentHandler struct {
	tmdbClient      tmdb.Client
	ratingsSvc      service.RatingsService
	adultEngine     *service.AdultDecisionEngine
	userRepo        repository.UserRepository
	jwtSecret       string
	watchmodeAPIKey string
	db              *mongo.Database

	homeCacheMu sync.RWMutex
	homeCache   map[int]*homeCacheEntry
}

func (h *ContentHandler) SetDatabase(db *mongo.Database) {
	h.db = db
	setGlobalLetterboxdDB(db)
}

func NewContentHandler(
	tmdbClient tmdb.Client,
	ratingsSvc service.RatingsService,
	adultEngine *service.AdultDecisionEngine,
	userRepo repository.UserRepository,
	jwtSecret string,
	watchmodeAPIKey ...string,
) *ContentHandler {
	watchmodeKey := ""
	if len(watchmodeAPIKey) > 0 {
		watchmodeKey = strings.TrimSpace(watchmodeAPIKey[0])
	}
	handler := &ContentHandler{
		tmdbClient:      tmdbClient,
		ratingsSvc:      ratingsSvc,
		adultEngine:     adultEngine,
		userRepo:        userRepo,
		jwtSecret:       jwtSecret,
		watchmodeAPIKey: watchmodeKey,
		homeCache:       make(map[int]*homeCacheEntry),
	}
	setGlobalLetterboxdContentHandler(handler)
	return handler
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
		// Trailers / videos (TMDB)
		cr.Get("/movies/{id}/videos", h.GetMovieVideos)
		cr.Get("/movie/{id}/videos", h.GetMovieVideos)
		cr.Get("/series/{id}/videos", h.GetSeriesVideos)
		cr.Get("/tv/{id}/videos", h.GetSeriesVideos)
		cr.Get("/series/{id}/season/{seasonNumber}/videos", h.GetSeasonVideos)
		cr.Get("/series/{id}/season/{seasonNumber}/episode/{episodeNumber}/videos", h.GetEpisodeVideos)

		// Person Details & Credits (both singular & plural aliases)
		cr.Get("/person/{id}", h.GetPersonDetail)
		cr.Get("/people/{id}", h.GetPersonDetail)
		cr.Get("/person/{id}/credits", h.GetPersonCredits)
		cr.Get("/people/{id}/credits", h.GetPersonCredits)

		// Feeds
		cr.Get("/trending", h.GetTrending)
		cr.Get("/popular", h.GetPopular)
		cr.Get("/upcoming", h.GetUpcoming)
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
		cr.Get("/search/collections", h.SearchTMDBCollections)
		cr.Get("/tmdb-collections/featured", h.GetFeaturedTMDBCollections)
		cr.Get("/collections/letterboxd", h.GetLetterboxdCollections)
		cr.Get("/letterboxd-collections", h.GetLetterboxdCollections)
		cr.Get("/letterboxd-collection/{slug}", h.GetLetterboxdCollection)
		cr.Post("/collections/letterboxd/import", h.ImportCustomLetterboxd)

		// Player sources & embed
		cr.Get("/player/sources", h.GetPlayerSources)
		cr.Get("/player/fallback", h.GetPlayerFallback)
		cr.Get("/scrape-embed", h.ScrapeEmbed)
	})
}

func (h *ContentHandler) GetSimilarMovies(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.getSimilarContent(w, r, id, "movie")
}

func (h *ContentHandler) GetSimilarSeries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.getSimilarContent(w, r, id, "tv")
}

func (h *ContentHandler) getSimilarContent(w http.ResponseWriter, r *http.Request, id, mediaType string) {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	parentEndpoint := fmt.Sprintf("/3/movie/%s", id)
	if mediaType == "tv" {
		parentEndpoint = fmt.Sprintf("/3/tv/%s", id)
	}
	adminMode := h.searchAdminMode(r)
	if parent, err := h.tmdbClient.Get(r.Context(), parentEndpoint, map[string]string{"language": "en-US"}); err == nil && parent != nil {
		if !searchItemVisible(parent, adminMode, h.adultEngine) || h.hasSuppressedCollectionPart(r, parent, adminMode) {
			writeJSON(w, http.StatusOK, emptySimilarResponse(page))
			return
		}
	}

	baseEndpoint := fmt.Sprintf("/3/movie/%s/similar", id)
	if mediaType == "tv" {
		baseEndpoint = fmt.Sprintf("/3/tv/%s/similar", id)
	}

	type pageResult struct {
		page int
		data map[string]any
		err  error
	}
	targetPages := []int{3*page - 2, 3*page - 1, 3*page}
	resultsCh := make(chan pageResult, len(targetPages))
	var wg sync.WaitGroup
	for _, p := range targetPages {
		pNum := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := h.tmdbClient.Get(r.Context(), baseEndpoint, map[string]string{
				"page": strconv.Itoa(pNum), "language": "en-US",
			})
			resultsCh <- pageResult{page: pNum, data: d, err: err}
		}()
	}
	wg.Wait()
	close(resultsCh)

	fetchedPages := make(map[int]map[string]any)
	tmdbTotalPages := 0
	tmdbTotalResults := 0
	for res := range resultsCh {
		if res.err == nil && res.data != nil {
			fetchedPages[res.page] = res.data
			if tp := int(getFloat(res.data["total_pages"])); tp > tmdbTotalPages {
				tmdbTotalPages = tp
			}
			if tr := int(getFloat(res.data["total_results"])); tr > tmdbTotalResults {
				tmdbTotalResults = tr
			}
		}
	}

	if len(fetchedPages) == 0 {
		writeJSON(w, http.StatusOK, emptySimilarResponse(page))
		return
	}

	filtered := make([]any, 0)
	seen := make(map[string]struct{})
	for _, p := range targetPages {
		data := fetchedPages[p]
		if data == nil {
			continue
		}
		rawResults, _ := data["results"].([]any)
		for _, raw := range rawResults {
			item, ok := raw.(map[string]any)
			if !ok || getFloat(item["id"]) == 0 || !searchItemVisible(item, adminMode, h.adultEngine) {
				continue
			}
			itemID := fmt.Sprint(item["id"])
			if itemID == id {
				continue
			}
			if _, exists := seen[itemID]; exists {
				continue
			}
			seen[itemID] = struct{}{}
			filtered = append(filtered, item)
		}
	}

	if h.ratingsSvc != nil {
		filtered = enrichSliceOfMaps(r.Context(), h.ratingsSvc, filtered)
	}

	soulstashTotalPages := (tmdbTotalPages + 2) / 3
	hasMore := len(filtered) > 0 && page < soulstashTotalPages

	if len(filtered) == 0 {
		soulstashTotalPages = max(0, page-1)
		tmdbTotalResults = 0
		hasMore = false
	} else if !hasMore {
		soulstashTotalPages = page
	}

	resp := map[string]any{
		"page":          page,
		"results":       filtered,
		"total_pages":   soulstashTotalPages,
		"total_results": tmdbTotalResults,
		"has_more":      hasMore,
		"hasMore":       hasMore,
	}
	writeJSON(w, http.StatusOK, resp)
}

func enrichSliceOfMaps(ctx context.Context, ratingsSvc service.RatingsService, rawList []any) []any {
	if len(rawList) == 0 || ratingsSvc == nil {
		return rawList
	}
	maps := make([]map[string]any, 0, len(rawList))
	for _, itm := range rawList {
		if m, ok := itm.(map[string]any); ok {
			maps = append(maps, m)
		}
	}
	if len(maps) == 0 {
		return rawList
	}
	enriched := ratingsSvc.BatchEnrich(ctx, maps)
	res := make([]any, len(enriched))
	for i, m := range enriched {
		res[i] = m
	}
	return res
}

func emptySimilarResponse(page int) map[string]any {
	return map[string]any{"page": page, "results": []any{}, "total_pages": 0, "total_results": 0, "has_more": false, "hasMore": false}
}

func (h *ContentHandler) hasSuppressedCollectionPart(r *http.Request, parent map[string]any, adminMode int) bool {
	collection, _ := parent["belongs_to_collection"].(map[string]any)
	if collection == nil || getFloat(collection["id"]) <= 0 {
		return false
	}
	collectionID := fmt.Sprint(collection["id"])
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/collection/%s", collectionID), map[string]string{"language": "en-US"})
	if err != nil || data == nil {
		return false
	}
	parts, _ := data["parts"].([]any)
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if ok && !searchItemVisible(part, adminMode, h.adultEngine) {
			return true
		}
	}
	return false
}

func (h *ContentHandler) GetTMDBCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/collection/%s", id), requestQueryParams(r))
	if err != nil || data == nil {
		// Return 200 with empty parts instead of 404 to avoid console error spam when a movie has an invalid collection ID
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": "", "parts": []any{}})
		return
	}
	adminMode := h.searchAdminMode(r)
	parts, _ := data["parts"].([]any)
	filtered := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if !searchItemVisible(part, adminMode, h.adultEngine) {
			continue
		}
		filtered = append(filtered, part)
	}
	if parts != nil {
		data["parts"] = filtered
	}
	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) GetPersonImages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.fetchProxy(w, r, fmt.Sprintf("/3/person/%s/images", id))
}

func (h *ContentHandler) GetPersonFilmography(w http.ResponseWriter, r *http.Request) {
	personID := chi.URLParam(r, "personId")
	if !regexp.MustCompile(`^nm\d+$`).MatchString(personID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid IMDb person ID"})
		return
	}
	items, err := scrapeIMDbFilmography(r, personID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Failed to scrape IMDB filmography", "details": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type htmlListItem struct {
	start int
	end   int
}

var (
	imdbTitleAnchorRE = regexp.MustCompile(`(?is)<a\b[^>]*href=["']((?:https?://(?:www\.)?imdb\.com)?/title/(tt\d+)/?[^"']*)["'][^>]*>(.*?)</a>`)
	htmlTagRE         = regexp.MustCompile(`(?is)<[^>]+>`)
	htmlListTokenRE   = regexp.MustCompile(`(?is)<li\b[^>]*>|</li\s*>`)
	htmlYearRE        = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)
	imdbRatingRE      = regexp.MustCompile(`(?is)class=["'][^"']*ipc-rating-star--rating[^"']*["'][^>]*>(.*?)</`)
	floatRE           = regexp.MustCompile(`\d+(?:\.\d+)?`)
)

func scrapeIMDbFilmography(r *http.Request, personID string) ([]map[string]any, error) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.imdb.com/name/"+personID+"/", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("IMDb returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	return parseIMDbFilmography(string(body)), nil
}

func parseIMDbFilmography(document string) []map[string]any {
	containers := make([]htmlListItem, 0)
	stack := make([]htmlListItem, 0)
	for _, token := range htmlListTokenRE.FindAllStringIndex(document, -1) {
		tag := strings.ToLower(document[token[0]:token[1]])
		if strings.HasPrefix(tag, "</li") {
			if len(stack) == 0 {
				continue
			}
			item := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			item.end = token[1]
			openingEnd := strings.IndexByte(document[item.start:item.end], '>')
			if openingEnd >= 0 {
				opening := strings.ToLower(document[item.start : item.start+openingEnd+1])
				if strings.Contains(opening, "ipc-metadata-list-summary-item") && strings.Contains(opening, "data-testid") {
					containers = append(containers, item)
				}
			}
			continue
		}
		stack = append(stack, htmlListItem{start: token[0]})
	}

	items := make([]map[string]any, 0)
	for _, anchor := range imdbTitleAnchorRE.FindAllStringSubmatchIndex(document, -1) {
		if len(anchor) < 8 {
			continue
		}
		title := normalizeIMDbText(document[anchor[6]:anchor[7]])
		imdbID := document[anchor[4]:anchor[5]]
		if title == "" {
			continue
		}
		var fragment string
		for _, container := range containers {
			if anchor[0] >= container.start && anchor[0] < container.end {
				fragment = document[container.start:container.end]
				break
			}
		}
		if fragment == "" {
			continue
		}

		item := map[string]any{"title": title, "year": nil, "imdb_rating": nil, "imdb_id": imdbID}
		plain := normalizeIMDbText(fragment)
		if year := htmlYearRE.FindString(plain); year != "" {
			if value, err := strconv.Atoi(year); err == nil {
				item["year"] = value
			}
		}
		if ratingMatch := imdbRatingRE.FindStringSubmatch(fragment); len(ratingMatch) > 1 {
			if rating := floatRE.FindString(normalizeIMDbText(ratingMatch[1])); rating != "" {
				if value, err := strconv.ParseFloat(rating, 64); err == nil {
					item["imdb_rating"] = value
				}
			}
		}
		items = append(items, item)
	}
	return items
}

func normalizeIMDbText(value string) string {
	value = html.UnescapeString(htmlTagRE.ReplaceAllString(value, " "))
	return strings.Join(strings.Fields(value), " ")
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
	clientTmdbPage := parsePositiveInt(r.URL.Query().Get("tmdb_page"), 0)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	targetCount := limit
	if r.URL.Query().Get("limit") == "" {
		targetCount = max(limit, 36)
	}
	if page == 1 && targetCount < 14 && r.URL.Query().Get("limit") == "" {
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

	movieParams := map[string]string{"language": "en-US", "include_adult": includeAdult}
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

	type fetchRes struct {
		page      int
		mediaType string
		data      map[string]any
		err       error
	}

	fetchBatch := func(pages []int) (map[int]map[string]any, map[int]map[string]any, int, int, bool) {
		ch := make(chan fetchRes, len(pages)*2)
		var wg sync.WaitGroup
		for _, p := range pages {
			pNum := p
			wg.Add(2)
			go func() {
				defer wg.Done()
				mParams := cloneStringMap(movieParams)
				mParams["page"] = strconv.Itoa(pNum)
				d, err := h.tmdbClient.Get(r.Context(), movieEndpoint, mParams)
				ch <- fetchRes{page: pNum, mediaType: "movie", data: d, err: err}
			}()
			go func() {
				defer wg.Done()
				tParams := cloneStringMap(tvParams)
				tParams["page"] = strconv.Itoa(pNum)
				d, err := h.tmdbClient.Get(r.Context(), tvEndpoint, tParams)
				ch <- fetchRes{page: pNum, mediaType: "tv", data: d, err: err}
			}()
		}
		wg.Wait()
		close(ch)

		mPages := make(map[int]map[string]any)
		tPages := make(map[int]map[string]any)
		maxP := 1
		totalR := 0
		success := false
		for res := range ch {
			if res.err == nil && res.data != nil {
				success = true
				if res.mediaType == "movie" {
					mPages[res.page] = res.data
				} else {
					tPages[res.page] = res.data
				}
				if tp := int(getFloat(res.data["total_pages"])); tp > maxP {
					maxP = tp
				}
				totalR += int(getFloat(res.data["total_results"]))
			}
		}
		return mPages, tPages, maxP, totalR, success
	}

	extractItemsFromPages := func(mPages, tPages map[int]map[string]any, pageList []int) []map[string]any {
		collected := make([]map[string]any, 0)
		for _, p := range pageList {
			if mD := mPages[p]; mD != nil {
				collected = append(collected, discoverItems(mD, "Movie", adminMode, h.adultEngine, false)...)
			}
			if tD := tPages[p]; tD != nil {
				collected = append(collected, discoverItems(tD, "Series", adminMode, h.adultEngine, false)...)
			}
		}
		return collected
	}

	items := make([]map[string]any, 0, targetCount)
	tmdbMaxPages := 1
	tmdbTotalResults := 0
	lastTmdbPage := 0
	hasSuccess := false

	if search != "" {
		targetPages := []int{page}
		mPages, tPages, maxP, totalR, ok := fetchBatch(targetPages)
		hasSuccess = ok
		tmdbMaxPages = maxP
		tmdbTotalResults = totalR
		items = append(items, extractItemsFromPages(mPages, tPages, targetPages)...)
		lastTmdbPage = page
	} else if clientTmdbPage == 0 {
		targetPages := []int{3*page - 2, 3*page - 1, 3*page}
		mPages, tPages, maxP, totalR, ok := fetchBatch(targetPages)
		hasSuccess = ok
		tmdbMaxPages = maxP
		tmdbTotalResults = totalR
		items = append(items, extractItemsFromPages(mPages, tPages, targetPages)...)
		lastTmdbPage = 3 * page
	} else {
		curPage := clientTmdbPage
		initialBatchSize := 2
		if adminMode == 2 {
			initialBatchSize = 10
		}
		firstBatch := make([]int, 0, initialBatchSize)
		for b := 0; b < initialBatchSize; b++ {
			firstBatch = append(firstBatch, curPage+b)
		}
		mPages, tPages, maxP, totalR, ok := fetchBatch(firstBatch)
		hasSuccess = ok
		tmdbMaxPages = maxP
		tmdbTotalResults = totalR
		lastTmdbPage = firstBatch[len(firstBatch)-1]
		pagesFetched := len(firstBatch)

		items = append(items, extractItemsFromPages(mPages, tPages, firstBatch)...)
		items = uniquePopularItems(items)
		if genreVal != "16" {
			items = withoutAnimeItems(items)
		}

		for len(items) < 36 && lastTmdbPage < tmdbMaxPages {
			maxPages := 5
			if len(items) < 18 {
				maxPages = 10
			}
			if adminMode == 2 {
				if len(items) < 18 {
					maxPages = tmdbMaxPages
				} else {
					maxPages = 20
				}
			}
			if pagesFetched >= maxPages {
				break
			}

			batchSize := 2
			if adminMode == 2 {
				batchSize = 10
			}
			if pagesFetched+batchSize > maxPages {
				batchSize = maxPages - pagesFetched
			}
			nextBatch := make([]int, 0, batchSize)
			for b := 0; b < batchSize; b++ {
				nextBatch = append(nextBatch, lastTmdbPage+1+b)
			}
			if len(nextBatch) == 0 {
				break
			}

			mP, tP, mp, tr, ok := fetchBatch(nextBatch)
			if ok {
				hasSuccess = true
			}
			if mp > tmdbMaxPages {
				tmdbMaxPages = mp
			}
			tmdbTotalResults += tr
			pagesFetched += len(nextBatch)
			lastTmdbPage = nextBatch[len(nextBatch)-1]

			newItems := extractItemsFromPages(mP, tP, nextBatch)
			if len(newItems) == 0 && tr == 0 && lastTmdbPage >= tmdbMaxPages {
				break
			}
			if len(newItems) > 0 {
				items = append(items, newItems...)
				items = uniquePopularItems(items)
				if genreVal != "16" {
					items = withoutAnimeItems(items)
				}
			}
		}
	}

	if !hasSuccess {
		writeError(w, http.StatusInternalServerError, "Failed to fetch from TMDB")
		return
	}

	items = uniquePopularItems(items)
	if genreVal != "16" {
		items = withoutAnimeItems(items)
	}
	if len(items) > targetCount {
		items = items[:targetCount]
	}

	var pages int
	if search != "" {
		pages = max(tmdbMaxPages, 500)
	} else {
		pages = (tmdbMaxPages + 2) / 3
		if pages < 1 {
			pages = 1
		}
		pages = min(pages, 500)
	}
	totalResults := max(tmdbTotalResults, pages*limit)

	if h.ratingsSvc != nil {
		items = h.ratingsSvc.BatchEnrich(r.Context(), items)
	}

	nextTmdbPage := lastTmdbPage + 1
	hasMore := lastTmdbPage < tmdbMaxPages
	writeJSON(w, http.StatusOK, map[string]any{
		"movies":  items,
		"results": items,
		"pagination": map[string]any{
			"page":           page,
			"limit":          len(items),
			"total":          totalResults,
			"pages":          pages,
			"next_tmdb_page": nextTmdbPage,
			"has_more":       hasMore,
		},
	})
}

func (h *ContentHandler) discoverBollywood(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	clientTmdbPage := parsePositiveInt(r.URL.Query().Get("tmdb_page"), 0)
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
	if limit > 100 {
		limit = 100
	}
	targetCount := limit
	if r.URL.Query().Get("limit") == "" {
		targetCount = max(limit, 36)
	}
	if page == 1 && targetCount < 14 && r.URL.Query().Get("limit") == "" {
		targetCount = 14
	}
	adminMode := h.searchAdminMode(r)

	type fetchRes struct {
		page      int
		mediaType string
		data      map[string]any
		err       error
	}

	fetchBollywoodBatch := func(pages []int, useDateWindow bool, window int) (map[int]map[string]any, map[int]map[string]any, int, int) {
		ch := make(chan fetchRes, len(pages)*2)
		var wg sync.WaitGroup

		startDate, endDate := "", ""
		if useDateWindow {
			startDate, endDate = fourMonthWindow(window)
		}

		for _, p := range pages {
			pNum := p
			wg.Add(2)
			go func() {
				defer wg.Done()
				common := map[string]string{
					"language":               "en-US",
					"page":                   strconv.Itoa(pNum),
					"include_adult":          strconv.FormatBool(adminMode != 0),
					"with_origin_country":    "IN",
					"with_original_language": "hi|ta|te|ml|kn|bn|mr|pa",
					"sort_by":                "popularity.desc",
				}
				if startDate != "" {
					common["primary_release_date.gte"] = startDate
					common["primary_release_date.lte"] = endDate
				}
				d, err := h.tmdbClient.Get(r.Context(), "/3/discover/movie", common)
				ch <- fetchRes{page: pNum, mediaType: "movie", data: d, err: err}
			}()
			go func() {
				defer wg.Done()
				common := map[string]string{
					"language":               "en-US",
					"page":                   strconv.Itoa(pNum),
					"include_adult":          strconv.FormatBool(adminMode != 0),
					"with_origin_country":    "IN",
					"with_original_language": "hi|ta|te|ml|kn|bn|mr|pa",
					"sort_by":                "popularity.desc",
				}
				if startDate != "" {
					common["first_air_date.gte"] = startDate
					common["first_air_date.lte"] = endDate
				}
				d, err := h.tmdbClient.Get(r.Context(), "/3/discover/tv", common)
				ch <- fetchRes{page: pNum, mediaType: "tv", data: d, err: err}
			}()
		}
		wg.Wait()
		close(ch)

		mPages := make(map[int]map[string]any)
		tPages := make(map[int]map[string]any)
		maxP := 1
		totalR := 0
		for res := range ch {
			if res.err == nil && res.data != nil {
				if res.mediaType == "movie" {
					mPages[res.page] = res.data
				} else {
					tPages[res.page] = res.data
				}
				if p := int(getFloat(res.data["total_pages"])); p > maxP {
					maxP = p
				}
				totalR += int(getFloat(res.data["total_results"]))
			}
		}
		return mPages, tPages, maxP, totalR
	}

	extractBollywood := func(mPages, tPages map[int]map[string]any, pList []int) []map[string]any {
		collected := make([]map[string]any, 0)
		for _, p := range pList {
			if mD := mPages[p]; mD != nil {
				collected = append(collected, discoverItems(mD, "Movie", adminMode, h.adultEngine, true)...)
			}
			if tD := tPages[p]; tD != nil {
				collected = append(collected, discoverItems(tD, "Series", adminMode, h.adultEngine, true)...)
			}
		}
		return collected
	}

	items := make([]map[string]any, 0, targetCount)
	lastTmdbPage := 0
	tmdbMaxPages := 500

	if adminMode == 2 {
		// In adult mode: call 10 TMDB pages in parallel!
		startP := clientTmdbPage
		if startP == 0 {
			startP = (page-1)*10 + 1
		}
		curP := startP
		pagesFetched := 0
		for len(items) < 36 && curP <= tmdbMaxPages {
			if len(items) >= 18 && pagesFetched >= 20 {
				break
			}
			batchSize := 10
			batch := make([]int, 0, batchSize)
			for b := 0; b < batchSize; b++ {
				batch = append(batch, curP+b)
			}
			mPages, tPages, maxP, _ := fetchBollywoodBatch(batch, false, 0)
			if maxP > 0 {
				tmdbMaxPages = maxP
			}
			curP += batchSize
			pagesFetched += batchSize
			lastTmdbPage = batch[len(batch)-1]

			newItems := extractBollywood(mPages, tPages, batch)
			if len(newItems) > 0 {
				items = append(items, newItems...)
				items = uniquePopularItems(items)
				items = withoutAnimeItems(items)
			}
		}
	} else {
		// Safe / Standard mode: fetch 4 pages in parallel per window
		currentWindow := (page - 1) / 3
		windowPages := []int{1, 2, 3, 4}
		if clientTmdbPage > 0 {
			windowPages = []int{clientTmdbPage, clientTmdbPage + 1, clientTmdbPage + 2, clientTmdbPage + 3}
			currentWindow = (clientTmdbPage - 1) / 4
		}
		mPages, tPages, maxP, _ := fetchBollywoodBatch(windowPages, true, currentWindow)
		if maxP > 0 {
			tmdbMaxPages = maxP
		}
		lastTmdbPage = windowPages[len(windowPages)-1]
		items = append(items, extractBollywood(mPages, tPages, windowPages)...)
		items = uniquePopularItems(items)
		items = withoutAnimeItems(items)

		if len(items) < targetCount {
			nextPages := []int{lastTmdbPage + 1, lastTmdbPage + 2, lastTmdbPage + 3, lastTmdbPage + 4}
			mP2, tP2, _, _ := fetchBollywoodBatch(nextPages, true, currentWindow+1)
			lastTmdbPage = nextPages[len(nextPages)-1]
			items = append(items, extractBollywood(mP2, tP2, nextPages)...)
			items = uniquePopularItems(items)
			items = withoutAnimeItems(items)
		}
	}

	if len(items) > targetCount {
		items = items[:targetCount]
	}
	if h.ratingsSvc != nil {
		items = h.ratingsSvc.BatchEnrich(r.Context(), items)
	}

	pages := max(500, page+20)
	nextTmdbPage := lastTmdbPage + 1
	hasMore := lastTmdbPage < tmdbMaxPages

	writeJSON(w, http.StatusOK, map[string]any{
		"movies":  items,
		"results": items,
		"pagination": map[string]any{
			"page":           page,
			"limit":          len(items),
			"total":          pages * limit,
			"pages":          pages,
			"next_tmdb_page": nextTmdbPage,
			"has_more":       hasMore,
		},
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
	h.filterDetailRelatedContent(r, data, true)

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
	ctx := r.Context()

	// 1. Fetch aggregate_credits which combines cast and crew across all seasons and episodes
	data, err := h.tmdbClient.Get(ctx, fmt.Sprintf("/3/tv/%s/aggregate_credits", id), requestQueryParams(r))
	if err != nil || data == nil || (data["cast"] == nil && data["crew"] == nil) {
		// Fallback to standard /credits proxy if aggregate_credits fails or is empty
		h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/credits", id))
		return
	}

	// 2. Score, rank, and sort the merged season/episode cast and crew
	h.processAndScoreSeriesCredits(data)

	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) processAndScoreSeriesCredits(data map[string]any) {
	if data == nil {
		return
	}

	// 1. Process Cast across seasons & episodes
	if rawCast, ok := data["cast"].([]any); ok && len(rawCast) > 0 {
		type rankedItem struct {
			item       map[string]any
			score      float64
			episodes   int
			order      int
			popularity float64
		}

		rankedCast := make([]rankedItem, 0, len(rawCast))

		for _, rc := range rawCast {
			cMap, ok := rc.(map[string]any)
			if !ok {
				continue
			}

			// Extract character names and episode counts across roles
			var charParts []string
			rolesEpisodes := 0
			if roles, ok := cMap["roles"].([]any); ok {
				for _, r := range roles {
					if rMap, ok := r.(map[string]any); ok {
						if ch, ok := rMap["character"].(string); ok && strings.TrimSpace(ch) != "" {
							charParts = append(charParts, strings.TrimSpace(ch))
						}
						rolesEpisodes += int(getFloat(rMap["episode_count"]))
					}
				}
			}

			if len(charParts) > 0 {
				cMap["character"] = strings.Join(charParts, " / ")
			}

			// Frequency: total episodes appeared in
			episodes := int(getFloat(cMap["total_episode_count"]))
			if episodes <= 0 {
				episodes = rolesEpisodes
			}
			if episodes <= 0 {
				episodes = 1
			}
			cMap["total_episode_count"] = episodes

			// Default coming order
			orderVal := 999
			if rawOrder, hasOrder := cMap["order"]; hasOrder {
				orderVal = int(getFloat(rawOrder))
				if orderVal < 0 {
					orderVal = 999
				}
			}

			pop := getFloat(cMap["popularity"])

			// Formula balancing frequency and default coming order:
			// - Order score: 1000 / (order + 1) -> top billed actors get strong priority
			// - Frequency score: total_episode_count * 15.0 -> actors appearing in many episodes get high score
			// - Popularity weight: popularity * 2.0 -> fine tie-breaking
			orderWeight := 1000.0 / float64(orderVal+1)
			freqWeight := float64(episodes) * 15.0
			popWeight := pop * 2.0
			totalScore := orderWeight + freqWeight + popWeight

			rankedCast = append(rankedCast, rankedItem{
				item:       cMap,
				score:      totalScore,
				episodes:   episodes,
				order:      orderVal,
				popularity: pop,
			})
		}

		// Sort by score descending
		sort.SliceStable(rankedCast, func(i, j int) bool {
			if math.Abs(rankedCast[i].score-rankedCast[j].score) > 0.01 {
				return rankedCast[i].score > rankedCast[j].score
			}
			if rankedCast[i].episodes != rankedCast[j].episodes {
				return rankedCast[i].episodes > rankedCast[j].episodes
			}
			if rankedCast[i].popularity != rankedCast[j].popularity {
				return rankedCast[i].popularity > rankedCast[j].popularity
			}
			return rankedCast[i].order < rankedCast[j].order
		})

		resultCast := make([]any, len(rankedCast))
		for i, rc := range rankedCast {
			rc.item["ranking"] = i + 1
			rc.item["score"] = math.Round(rc.score*10) / 10
			resultCast[i] = rc.item
		}
		data["cast"] = resultCast
	}

	// 2. Process Crew across seasons & episodes
	if rawCrew, ok := data["crew"].([]any); ok && len(rawCrew) > 0 {
		type rankedCrewItem struct {
			item       map[string]any
			score      float64
			episodes   int
			popularity float64
		}

		rankedCrew := make([]rankedCrewItem, 0, len(rawCrew))

		for _, rc := range rawCrew {
			cMap, ok := rc.(map[string]any)
			if !ok {
				continue
			}

			var jobParts []string
			jobsEpisodes := 0
			if jobs, ok := cMap["jobs"].([]any); ok {
				for _, j := range jobs {
					if jMap, ok := j.(map[string]any); ok {
						if jTitle, ok := jMap["job"].(string); ok && strings.TrimSpace(jTitle) != "" {
							jobParts = append(jobParts, strings.TrimSpace(jTitle))
						}
						jobsEpisodes += int(getFloat(jMap["episode_count"]))
					}
				}
			}

			if len(jobParts) > 0 {
				cMap["job"] = strings.Join(jobParts, ", ")
			}

			episodes := int(getFloat(cMap["total_episode_count"]))
			if episodes <= 0 {
				episodes = jobsEpisodes
			}
			if episodes <= 0 {
				episodes = 1
			}
			cMap["total_episode_count"] = episodes

			pop := getFloat(cMap["popularity"])
			totalScore := (float64(episodes) * 15.0) + (pop * 2.0)

			rankedCrew = append(rankedCrew, rankedCrewItem{
				item:       cMap,
				score:      totalScore,
				episodes:   episodes,
				popularity: pop,
			})
		}

		sort.SliceStable(rankedCrew, func(i, j int) bool {
			if math.Abs(rankedCrew[i].score-rankedCrew[j].score) > 0.01 {
				return rankedCrew[i].score > rankedCrew[j].score
			}
			if rankedCrew[i].episodes != rankedCrew[j].episodes {
				return rankedCrew[i].episodes > rankedCrew[j].episodes
			}
			return rankedCrew[i].popularity > rankedCrew[j].popularity
		})

		resultCrew := make([]any, len(rankedCrew))
		for i, rc := range rankedCrew {
			rc.item["ranking"] = i + 1
			rc.item["score"] = math.Round(rc.score*10) / 10
			resultCrew[i] = rc.item
		}
		data["crew"] = resultCrew
	}
}

func (h *ContentHandler) GetSeriesDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	params := map[string]string{"append_to_response": "videos,credits,recommendations,external_ids,images", "include_image_language": "en,null"}
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/tv/%s", id), params)
	if err != nil {
		writeError(w, http.StatusNotFound, "Series not found")
		return
	}
	ksMergeSeriesDetail(r.Context(), data)
	h.filterDetailRelatedContent(r, data, false)

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
	h.seriesSeasonWithKartoons(w, r, id, season)
}

func (h *ContentHandler) GetPersonDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	params := map[string]string{"append_to_response": "combined_credits,images"}
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/person/%s", id), params)
	if err != nil {
		writeError(w, http.StatusNotFound, "Person not found")
		return
	}
	if credits, ok := data["combined_credits"].(map[string]any); ok {
		h.filterPersonCredits(credits, h.searchAdminMode(r))
		if h.ratingsSvc != nil {
			if cast, ok := credits["cast"].([]any); ok {
				credits["cast"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, cast)
			}
			if crew, ok := credits["crew"].([]any); ok {
				credits["crew"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, crew)
			}
		}
	}
	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) GetPersonCredits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/person/%s/combined_credits", id), requestQueryParams(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "Person credits not found")
		return
	}
	h.filterPersonCredits(data, h.searchAdminMode(r))
	if h.ratingsSvc != nil {
		if cast, ok := data["cast"].([]any); ok {
			data["cast"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, cast)
		}
		if crew, ok := data["crew"].([]any); ok {
			data["crew"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, crew)
		}
	}
	writeJSON(w, http.StatusOK, data)
}

func (h *ContentHandler) GetTrending(w http.ResponseWriter, r *http.Request) {
	adminMode := h.searchAdminMode(r)
	if adminMode == 2 {
		q := r.URL.Query()
		if q.Get("sort_by") == "" {
			q.Set("sort_by", "popularity.desc")
		}
		r.URL.RawQuery = q.Encode()
		h.DiscoverMovies(w, r)
		return
	}

	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	timeWindow := r.URL.Query().Get("time_window")
	if timeWindow == "" {
		timeWindow = "day"
	}

	p1 := 2*page - 1
	p2 := 2 * page

	type fetchRes struct {
		page int
		data map[string]any
		err  error
	}

	ch := make(chan fetchRes, 2)
	var wg sync.WaitGroup

	fetchPage := func(p int, tw string) {
		defer wg.Done()
		params := requestQueryParams(r)
		params["page"] = strconv.Itoa(p)
		d, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/trending/all/%s", tw), params)
		ch <- fetchRes{page: p, data: d, err: err}
	}

	wg.Add(2)
	go fetchPage(p1, timeWindow)
	go fetchPage(p2, timeWindow)
	wg.Wait()
	close(ch)

	resultsMap := make(map[int]map[string]any)
	maxPages := 500
	totalResults := 1000
	for res := range ch {
		if res.err == nil && res.data != nil {
			resultsMap[res.page] = res.data
			if tp := int(getFloat(res.data["total_pages"])); tp > 0 && (tp < maxPages || maxPages == 500) {
				maxPages = tp
			}
			if tr := int(getFloat(res.data["total_results"])); tr > 0 {
				totalResults = tr
			}
		}
	}

	var combined []any
	for _, p := range []int{p1, p2} {
		if d := resultsMap[p]; d != nil {
			if resList, ok := d["results"].([]any); ok {
				combined = append(combined, resList...)
			}
		}
	}

	if len(combined) == 0 && timeWindow == "day" && p1 > 10 {
		weekData, err := h.tmdbClient.Get(r.Context(), "/3/trending/all/week", map[string]string{
			"page": strconv.Itoa(max(1, page-10)),
		})
		if err == nil && weekData != nil {
			if resList, ok := weekData["results"].([]any); ok {
				combined = append(combined, resList...)
			}
		}
	}

	payload := map[string]any{"results": combined}
	h.filterContentResults(payload, adminMode)

	filteredResults, _ := payload["results"].([]any)
	if h.ratingsSvc != nil && len(filteredResults) > 0 {
		filteredResults = enrichSliceOfMaps(r.Context(), h.ratingsSvc, filteredResults)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"page":          page,
		"results":       filteredResults,
		"movies":        filteredResults,
		"total_pages":   max(maxPages, 500),
		"total_results": totalResults,
	})
}

func (h *ContentHandler) GetPopular(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if mediaType == "tv" || mediaType == "series" {
		data, err := h.tmdbClient.Get(r.Context(), "/3/tv/popular", requestQueryParams(r))
		if err != nil {
			writeError(w, http.StatusBadGateway, "Failed to fetch popular content")
			return
		}
		h.filterContentResults(data, h.searchAdminMode(r))
		if h.ratingsSvc != nil {
			if results, ok := data["results"].([]any); ok {
				data["results"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, results)
			}
		}
		writeJSON(w, http.StatusOK, data)
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
	if r.URL.Query().Get("limit") == "" {
		targetCount = max(limit, 36)
	}
	if page == 1 && targetCount < 14 && r.URL.Query().Get("limit") == "" {
		targetCount = 14
	}
	adminMode := h.searchAdminMode(r)
	today := time.Now().UTC().Format("2006-01-02")

	movieParams := map[string]string{
		"language":                 "en-US",
		"include_adult":            strconv.FormatBool(adminMode != 0),
		"sort_by":                  "popularity.desc",
		"primary_release_date.lte": today,
	}
	seriesParams := cloneStringMap(movieParams)
	delete(seriesParams, "primary_release_date.lte")
	seriesParams["first_air_date.lte"] = today

	targetPages := []int{3*page - 2, 3*page - 1, 3*page}

	type fetchRes struct {
		page      int
		mediaType string
		data      map[string]any
		err       error
	}

	resultsCh := make(chan fetchRes, len(targetPages)*2)
	var wg sync.WaitGroup

	for _, p := range targetPages {
		pNum := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			mParams := cloneStringMap(movieParams)
			mParams["page"] = strconv.Itoa(pNum)
			data, err := h.tmdbClient.Get(r.Context(), "/3/discover/movie", mParams)
			resultsCh <- fetchRes{page: pNum, mediaType: "movie", data: data, err: err}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			sParams := cloneStringMap(seriesParams)
			sParams["page"] = strconv.Itoa(pNum)
			data, err := h.tmdbClient.Get(r.Context(), "/3/discover/tv", sParams)
			resultsCh <- fetchRes{page: pNum, mediaType: "tv", data: data, err: err}
		}()
	}

	wg.Wait()
	close(resultsCh)

	moviePageData := make(map[int]map[string]any)
	seriesPageData := make(map[int]map[string]any)
	moviePages, seriesPages := 1, 1
	movieTotal, seriesTotal := 0, 0
	hasSuccess := false

	for res := range resultsCh {
		if res.err == nil && res.data != nil {
			hasSuccess = true
			if res.mediaType == "movie" {
				moviePageData[res.page] = res.data
				if p := int(getFloat(res.data["total_pages"])); p > moviePages {
					moviePages = p
				}
				movieTotal += int(getFloat(res.data["total_results"]))
			} else {
				seriesPageData[res.page] = res.data
				if p := int(getFloat(res.data["total_pages"])); p > seriesPages {
					seriesPages = p
				}
				seriesTotal += int(getFloat(res.data["total_results"]))
			}
		}
	}

	if !hasSuccess {
		writeError(w, http.StatusBadGateway, "Failed to fetch popular content")
		return
	}

	items := make([]map[string]any, 0, targetCount)
	for _, p := range targetPages {
		pageItems := make([]map[string]any, 0)
		if mD := moviePageData[p]; mD != nil {
			pageItems = append(pageItems, discoverItems(mD, "Movie", adminMode, h.adultEngine, false)...)
		}
		if sD := seriesPageData[p]; sD != nil {
			pageItems = append(pageItems, discoverItems(sD, "Series", adminMode, h.adultEngine, false)...)
		}
		pageItems = withoutAnimeItems(pageItems)
		items = uniquePopularItems(append(items, pageItems...))
	}

	if len(items) > targetCount {
		items = items[:targetCount]
	}

	if h.ratingsSvc != nil {
		items = h.ratingsSvc.BatchEnrich(r.Context(), items)
	}

	tmdbMaxPages := max(moviePages, seriesPages)
	safeTotalPages := (tmdbMaxPages + 2) / 3
	if safeTotalPages < 1 {
		safeTotalPages = 1
	}
	safeTotalPages = min(safeTotalPages, 500)
	totalResults := movieTotal + seriesTotal
	if totalResults < safeTotalPages*limit {
		totalResults = safeTotalPages * limit
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"movies":        items,
		"results":       items,
		"page":          page,
		"total_pages":   safeTotalPages,
		"total_results": totalResults,
		"pagination": map[string]int{
			"page": page, "limit": len(items), "total": totalResults, "pages": safeTotalPages,
		},
	})
}

var anticipatedUpcomingMovieIDs = []int{
	1170608, // Dune: Part Three
	911916,  // Spider-Man: Beyond the Spider-Verse
	1003596, // Avengers: Doomsday
	806704,  // The Batman: Part II
	1421903, // Werwulf
	1003598, // Avengers: Secret Wars
	1457866, // THE FURTHER MIS-ADVENTURES OF CLIFF BOOTH
	421892,  // Shrek 5
	1147572, // Narnia: The Magician's Nephew
	216527,  // Avatar 4
	1523140, // Man of Tomorrow
	1273002, // 28 Years Later: Part III
	393209,  // Avatar 5
	1047807, // Heat 2
	1260649, // Jumanji: Open World
	1198577, // They Follow
	1540936, // Anything but Ghosts
	1297467, // The Exorcist: Martyrs
	1417668, // Star Wars: Starfighter
	1327821, // Incredibles 3
	1485839, // Elden Ring
	1177369, // The Legend of Zelda
	1320786, // Terrifier 4
	1024604, // Frozen III
	1401586, // Sonic the Hedgehog 4
	755679,  // Fast Forever
	1090869, // The Lord of the Rings: The Hunt for Gollum
	701244,  // The Take
	1497464, // Gatto
	1077873, // The Princess Diaries 3
	1293690, // Untitled X-Men Film
	1384758, // Ice Age: Boiling Point
	1280608, // Evil Dead Wrath
	1300968, // The Hunger Games: Sunrise on the Reaping
	1300926, // The Angry Birds Movie 3
}

var (
	anticipatedUpcomingMu    sync.RWMutex
	anticipatedUpcomingCache []map[string]any
	anticipatedUpcomingTime  time.Time
)

func (h *ContentHandler) getAnticipatedUpcoming(ctx context.Context) []map[string]any {
	anticipatedUpcomingMu.RLock()
	if len(anticipatedUpcomingCache) > 0 && time.Since(anticipatedUpcomingTime) < 6*time.Hour {
		cached := anticipatedUpcomingCache
		anticipatedUpcomingMu.RUnlock()
		return cached
	}
	anticipatedUpcomingMu.RUnlock()

	anticipatedUpcomingMu.Lock()
	defer anticipatedUpcomingMu.Unlock()

	if len(anticipatedUpcomingCache) > 0 && time.Since(anticipatedUpcomingTime) < 6*time.Hour {
		return anticipatedUpcomingCache
	}

	items := make([]map[string]any, len(anticipatedUpcomingMovieIDs))
	var wg sync.WaitGroup
	for i, id := range anticipatedUpcomingMovieIDs {
		wg.Add(1)
		go func(idx int, movieID int) {
			defer wg.Done()
			detail, err := h.tmdbClient.Get(ctx, fmt.Sprintf("/3/movie/%d", movieID), map[string]string{"language": "en-US"})
			if err == nil && detail != nil {
				poster, _ := detail["poster_path"].(string)
				if poster == "" {
					return
				}
				releaseDate, _ := detail["release_date"].(string)
				title, _ := detail["title"].(string)
				items[idx] = map[string]any{
					"id":            detail["id"],
					"title":         title,
					"name":          title,
					"release_date":  releaseDate,
					"poster_path":   poster,
					"backdrop_path": detail["backdrop_path"],
					"overview":      detail["overview"],
					"popularity":    detail["popularity"],
					"vote_average":  detail["vote_average"],
					"vote_count":    detail["vote_count"],
					"media_type":    "Movie",
				}
			}
		}(i, id)
	}
	wg.Wait()

	valid := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if it != nil {
			valid = append(valid, it)
		}
	}

	anticipatedUpcomingCache = valid
	anticipatedUpcomingTime = time.Now()
	return valid
}

func discoverUpcomingItems(data map[string]any, adminMode int) []map[string]any {
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
		adult := item["adult"] == true
		if adminMode == 0 && adult {
			continue
		}
		if adminMode == 2 && !adult {
			continue
		}
		item["media_type"] = "Movie"
		items = append(items, item)
	}
	return items
}

// GetUpcoming returns movies that have not been released yet, sorted by popularity (most anticipated).
// Page 1 features the most anticipated blockbuster films (matching the Letterboxd anticipated list),
// followed by TMDB discover upcoming films. Subsequent pages provide infinite scroll from TMDB discover.
func (h *ContentHandler) GetUpcoming(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	adminMode := h.searchAdminMode(r)
	today := time.Now().UTC().Format("2006-01-02")

	anticipated := h.getAnticipatedUpcoming(r.Context())
	anticipatedSet := make(map[string]bool, len(anticipated))
	for _, a := range anticipated {
		if id := a["id"]; id != nil {
			anticipatedSet[fmt.Sprint(id)] = true
		}
	}

	targetCount := 20
	if page == 1 {
		targetCount = len(anticipated) + 15
	}

	items := make([]map[string]any, 0, targetCount)
	seen := make(map[string]bool)

	if page == 1 {
		for _, a := range anticipated {
			idStr := fmt.Sprint(a["id"])
			if !seen[idStr] {
				seen[idStr] = true
				items = append(items, a)
			}
		}
	}

	currentTmdbPage := page
	totalPages, totalResults := 1, 0

	for attempts := 0; len(items) < targetCount && attempts < 4; attempts++ {
		params := map[string]string{
			"language":                 "en-US",
			"page":                     strconv.Itoa(currentTmdbPage),
			"include_adult":            strconv.FormatBool(adminMode == 2),
			"sort_by":                  "popularity.desc",
			"primary_release_date.gte": today,
		}
		data, err := h.tmdbClient.Get(r.Context(), "/3/discover/movie", params)
		if err != nil {
			break
		}

		totalPages = max(totalPages, int(getFloat(data["total_pages"])))
		totalResults = max(totalResults, int(getFloat(data["total_results"])))

		discover := discoverUpcomingItems(data, adminMode)
		for _, d := range discover {
			idStr := fmt.Sprint(d["id"])
			if page > 1 && anticipatedSet[idStr] {
				continue
			}
			if !seen[idStr] {
				seen[idStr] = true
				items = append(items, d)
				if len(items) >= targetCount {
					break
				}
			}
		}
		currentTmdbPage++
	}

	if totalPages < 1 {
		totalPages = 500
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"movies":        items,
		"results":       items,
		"page":          page,
		"total_pages":   totalPages,
		"total_results": totalResults,
		"pagination": map[string]int{
			"page": page, "limit": len(items), "total": totalResults, "pages": totalPages,
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

	hiddenGenres := map[float64]bool{
		10763: true, // News
		10764: true, // Reality
		10766: true, // Soap
		10767: true, // Talk
	}

	if movieData != nil {
		if genres, ok := movieData["genres"].([]any); ok {
			for _, g := range genres {
				if gMap, ok := g.(map[string]any); ok {
					id := getFloat(gMap["id"])
					if hiddenGenres[id] {
						continue
					}
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
					if hiddenGenres[id] {
						continue
					}
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
	adminMode := h.searchAdminMode(r)

	// In-memory cache lookup
	h.homeCacheMu.RLock()
	cached, ok := h.homeCache[adminMode]
	if ok && cached != nil && time.Now().Before(cached.expiresAt) {
		h.homeCacheMu.RUnlock()
		writeJSON(w, http.StatusOK, cached.data)
		return
	}
	h.homeCacheMu.RUnlock()

	// In adult mode: fetch discovery adult feeds in 10-page parallel batches until at least 18 titles are found
	if adminMode == 2 {
		trendingItems := make([]map[string]any, 0, 36)
		popularMovieItems := make([]map[string]any, 0, 36)
		popularSeriesItems := make([]map[string]any, 0, 36)

		curP := 1
		tmdbMaxP := 500
		pagesFetched := 0

		type fetchRes struct {
			page      int
			mediaType string
			data      map[string]any
		}

		for len(trendingItems) < 36 && curP <= tmdbMaxP {
			if len(trendingItems) >= 18 && pagesFetched >= 20 {
				break
			}
			batchSize := 10
			batch := make([]int, 0, batchSize)
			for b := 0; b < batchSize; b++ {
				batch = append(batch, curP+b)
			}
			curP += batchSize
			pagesFetched += batchSize

			ch := make(chan fetchRes, len(batch)*2)
			var wg sync.WaitGroup
			for _, p := range batch {
				pNum := p
				wg.Add(2)
				go func() {
					defer wg.Done()
					d, _ := h.tmdbClient.Get(r.Context(), "/3/discover/movie", map[string]string{
						"page":          strconv.Itoa(pNum),
						"include_adult": "true",
						"sort_by":       "popularity.desc",
					})
					ch <- fetchRes{page: pNum, mediaType: "movie", data: d}
				}()
				go func() {
					defer wg.Done()
					d, _ := h.tmdbClient.Get(r.Context(), "/3/discover/tv", map[string]string{
						"page":          strconv.Itoa(pNum),
						"include_adult": "true",
						"sort_by":       "popularity.desc",
					})
					ch <- fetchRes{page: pNum, mediaType: "tv", data: d}
				}()
			}
			wg.Wait()
			close(ch)

			for res := range ch {
				if res.data != nil {
					if tp := int(getFloat(res.data["total_pages"])); tp > 0 && tp < tmdbMaxP {
						tmdbMaxP = tp
					}
					extracted := discoverItems(res.data, res.mediaType, adminMode, h.adultEngine, false)
					for _, itm := range extracted {
						if res.mediaType == "movie" {
							popularMovieItems = append(popularMovieItems, itm)
						} else {
							popularSeriesItems = append(popularSeriesItems, itm)
						}
						trendingItems = append(trendingItems, itm)
					}
				}
			}

			trendingItems = uniquePopularItems(trendingItems)
			popularMovieItems = uniquePopularItems(popularMovieItems)
			popularSeriesItems = uniquePopularItems(popularSeriesItems)
		}

		if len(trendingItems) > 36 {
			trendingItems = trendingItems[:36]
		}
		if len(popularMovieItems) > 36 {
			popularMovieItems = popularMovieItems[:36]
		}
		if len(popularSeriesItems) > 36 {
			popularSeriesItems = popularSeriesItems[:36]
		}

		if h.ratingsSvc != nil {
			trendingItems = h.ratingsSvc.BatchEnrich(r.Context(), trendingItems)
			popularMovieItems = h.ratingsSvc.BatchEnrich(r.Context(), popularMovieItems)
			popularSeriesItems = h.ratingsSvc.BatchEnrich(r.Context(), popularSeriesItems)
		}

		payload := map[string]any{
			"trending":      map[string]any{"results": trendingItems},
			"popular":       map[string]any{"results": trendingItems},
			"popularMovies": map[string]any{"results": popularMovieItems},
			"popularSeries": map[string]any{"results": popularSeriesItems},
		}

		// Cache adult home data for 30 minutes
		h.homeCacheMu.Lock()
		h.homeCache[adminMode] = &homeCacheEntry{
			data:      payload,
			expiresAt: time.Now().Add(30 * time.Minute),
		}
		h.homeCacheMu.Unlock()

		writeJSON(w, http.StatusOK, payload)
		return
	}

	type fetchRes struct {
		name string
		data map[string]any
		err  error
	}

	var wg sync.WaitGroup
	ch := make(chan fetchRes, 4)

	// Fetch 2 pages of trending in parallel so there are at least 30-40 items before filtering (guaranteeing >= 14 items)
	wg.Add(4)
	go func() {
		defer wg.Done()
		d, err := h.tmdbClient.Get(r.Context(), "/3/trending/all/day", map[string]string{"page": "1"})
		ch <- fetchRes{name: "trending1", data: d, err: err}
	}()
	go func() {
		defer wg.Done()
		d, err := h.tmdbClient.Get(r.Context(), "/3/trending/all/day", map[string]string{"page": "2"})
		ch <- fetchRes{name: "trending2", data: d, err: err}
	}()
	go func() {
		defer wg.Done()
		d, err := h.tmdbClient.Get(r.Context(), "/3/movie/popular", nil)
		ch <- fetchRes{name: "popularMovies", data: d, err: err}
	}()
	go func() {
		defer wg.Done()
		d, err := h.tmdbClient.Get(r.Context(), "/3/tv/popular", nil)
		ch <- fetchRes{name: "popularSeries", data: d, err: err}
	}()
	wg.Wait()
	close(ch)

	var trending, popularMovies, popularSeries map[string]any
	var trendingResults []any

	for res := range ch {
		if res.err != nil || res.data == nil {
			continue
		}
		switch res.name {
		case "trending1":
			trending = res.data
			if r1, ok := res.data["results"].([]any); ok {
				trendingResults = append(trendingResults, r1...)
			}
		case "trending2":
			if r2, ok := res.data["results"].([]any); ok {
				trendingResults = append(trendingResults, r2...)
			}
		case "popularMovies":
			popularMovies = res.data
		case "popularSeries":
			popularSeries = res.data
		}
	}

	if trending == nil {
		trending = map[string]any{"results": []any{}}
	}
	trending["results"] = trendingResults

	h.filterContentResults(trending, adminMode)
	h.filterContentResults(popularMovies, adminMode)
	h.filterContentResults(popularSeries, adminMode)

	if h.ratingsSvc != nil {
		if results, ok := trending["results"].([]any); ok {
			trending["results"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, results)
		}
		if popularMovies != nil {
			if results, ok := popularMovies["results"].([]any); ok {
				popularMovies["results"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, results)
			}
		}
		if popularSeries != nil {
			if results, ok := popularSeries["results"].([]any); ok {
				popularSeries["results"] = enrichSliceOfMaps(r.Context(), h.ratingsSvc, results)
			}
		}
	}

	payload := map[string]any{
		"trending":      trending,
		"popularMovies": popularMovies,
		"popularSeries": popularSeries,
	}

	// Cache home data for 15 minutes
	h.homeCacheMu.Lock()
	h.homeCache[adminMode] = &homeCacheEntry{
		data:      payload,
		expiresAt: time.Now().Add(15 * time.Minute),
	}
	h.homeCacheMu.Unlock()

	writeJSON(w, http.StatusOK, payload)
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
	if adminMode == 2 {
		if item["_isAdultDiscover"] == true || item["adult"] == true {
			return true
		}
		if adultEngine != nil {
			return adultEngine.HasAdultKeywords(item)
		}
		return item["adult"] == true
	}
	if adultEngine != nil {
		return !adultEngine.Evaluate(item)
	}
	return item["adult"] != true
}

func (h *ContentHandler) filterContentResults(payload map[string]any, adminMode int) {
	if payload == nil {
		return
	}
	results, ok := payload["results"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(results))
	for _, raw := range results {
		item, ok := raw.(map[string]any)
		if ok && searchItemVisible(item, adminMode, h.adultEngine) {
			filtered = append(filtered, item)
		}
	}
	payload["results"] = filtered
}

func (h *ContentHandler) filterPersonCredits(payload map[string]any, adminMode int) {
	if payload == nil {
		return
	}
	for _, key := range []string{"cast", "crew"} {
		credits, ok := payload[key].([]any)
		if !ok {
			continue
		}
		filtered := make([]any, 0, len(credits))
		for _, raw := range credits {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			mediaType, _ := item["media_type"].(string)
			if mediaType != "movie" && mediaType != "tv" {
				continue
			}
			if !searchItemVisible(item, adminMode, h.adultEngine) {
				continue
			}
			imdbID, _ := item["imdb_id"].(string)
			if getFloat(item["vote_count"]) <= 0 && strings.TrimSpace(imdbID) == "" {
				continue
			}
			releaseDate, _ := item["release_date"].(string)
			if releaseDate == "" {
				releaseDate, _ = item["first_air_date"].(string)
			}
			if strings.TrimSpace(releaseDate) == "" {
				continue
			}
			filtered = append(filtered, item)
		}
		payload[key] = filtered
	}
}

func (h *ContentHandler) filterDetailRelatedContent(r *http.Request, detail map[string]any, movie bool) {
	if detail == nil {
		return
	}
	adminMode := h.searchAdminMode(r)
	parentSuppressed := !searchItemVisible(detail, adminMode, h.adultEngine)
	collectionSuppressed := movie && h.hasSuppressedCollectionPart(r, detail, adminMode)
	if parentSuppressed || collectionSuppressed {
		if movie {
			if _, exists := detail["belongs_to_collection"]; exists {
				detail["belongs_to_collection"] = nil
			}
		}
		for _, key := range []string{"similar", "recommendations"} {
			if related, ok := detail[key].(map[string]any); ok {
				detail[key] = emptiedRelatedResults(related)
			}
		}
		return
	}
	for _, key := range []string{"similar", "recommendations"} {
		if related, ok := detail[key].(map[string]any); ok {
			h.filterContentResults(related, adminMode)
		}
	}
}

func emptiedRelatedResults(related map[string]any) map[string]any {
	filtered := make(map[string]any, len(related)+1)
	for key, value := range related {
		filtered[key] = value
	}
	filtered["results"] = []any{}
	filtered["total_pages"] = 0
	filtered["total_results"] = 0
	return filtered
}

func requestQueryParams(r *http.Request) map[string]string {
	params := make(map[string]string)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	return params
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
	tmdbID := strings.TrimSpace(r.URL.Query().Get("tmdbId"))
	parsedID, err := strconv.ParseInt(tmdbID, 10, 64)
	if err != nil || parsedID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tmdbId is required."})
		return
	}
	mediaType := "series"
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("mediaType")), "movie") {
		mediaType = "movie"
	}
	season, episode := 0, 0
	if mediaType == "series" {
		season = parsePositiveInt(r.URL.Query().Get("seasonNumber"), 1)
		episode = parsePositiveInt(r.URL.Query().Get("episodeNumber"), 1)
	}

	var record map[string]any
	if h.db != nil {
		var stored bson.M
		if err := h.db.Collection(mongodb.CollectionPlayerSources).FindOne(r.Context(), bson.M{
			"tmdbId": parsedID, "mediaType": mediaType,
		}).Decode(&stored); err == nil {
			record = map[string]any(stored)
		}
	}

	sourceRecord := record
	if mediaType == "series" {
		episodes := asContentMap(record["episodes"])
		if episodeRecord := asContentMap(episodes[fmt.Sprintf("s%de%d", season, episode)]); episodeRecord != nil {
			sourceRecord = episodeRecord
		}
	}
	providerMap := asContentMap(sourceRecord["sources"])
	sources := make([]map[string]any, 0)
	knownProviders := map[string]struct{}{}
	preferredProviders := []string{"smwh", "rpmshre", "upnshr", "strmp2", "flls"}
	for index, key := range preferredProviders {
		knownProviders[key] = struct{}{}
		urls := sanitizePlayerProviderURLs(providerMap[key])
		if len(urls) == 0 {
			continue
		}
		sources = append(sources, map[string]any{
			"id": key, "key": key, "label": fmt.Sprintf("H%d", index+1),
			"urls": urls, "url": urls[0], "embeddable": true,
		})
	}

	var youtubeURL string
	if urls := sanitizePlayerProviderURLs(providerMap["youtube"]); len(urls) > 0 && isYouTubeFullSource(sourceRecord, 0, mediaType) {
		youtubeURL = urls[0]
	}
	knownProviders["youtube"] = struct{}{}
	if youtubeURL == "" {
		if video, identity := h.findFullYouTubeVideo(r.Context(), tmdbID, mediaType, season, episode); video != nil {
			youtubeURL = youtubeWatchURL(video.ID)
			if sourceRecord == nil {
				sourceRecord = make(map[string]any)
			}
			sourceRecord["youtubeDurationSeconds"] = video.Seconds
			sourceRecord["youtubeTargetRuntimeMinutes"] = identity.runtimeMinutes
			sourceRecord["youtubeTargetYear"] = identity.year
			sourceRecord["youtubeTitle"] = video.Title
			sourceRecord["updatedAt"] = time.Now()
			h.persistYouTubeSource(r.Context(), parsedID, identity, *video)
		}
	}
	if youtubeURL == "" {
		endpoints := make([]string, 0, 3)
		if mediaType == "series" {
			endpoints = append(endpoints, fmt.Sprintf("/3/tv/%s/season/%d/episode/%d/videos", tmdbID, season, episode))
			if season != 1 || episode != 1 {
				endpoints = append(endpoints, fmt.Sprintf("/3/tv/%s/season/1/episode/1/videos", tmdbID))
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
			youtubeURL = fmt.Sprintf("https://www.youtube-nocookie.com/embed/%s", trailerKey)
		}
	}
	if youtubeURL != "" {
		sources = append(sources, map[string]any{
			"id": "youtube", "key": "youtube", "label": "YouTube",
			"url": youtubeURL, "urls": []string{youtubeURL}, "embeddable": true,
		})
	}

	dynamicKeys := make([]string, 0, len(providerMap))
	for key := range providerMap {
		if _, exists := knownProviders[key]; !exists {
			dynamicKeys = append(dynamicKeys, key)
		}
	}
	sort.Strings(dynamicKeys)
	for _, key := range dynamicKeys {
		urls := sanitizePlayerProviderURLs(providerMap[key])
		if len(urls) > 0 {
			sources = append(sources, map[string]any{
				"id": key, "key": key, "label": key, "urls": urls, "url": urls[0], "embeddable": true,
			})
		}
	}

	downloads := contentStringList(sourceRecord["downloads"])
	searchKey, _ := record["searchKey"].(string)
	imdbID := strings.TrimSpace(r.URL.Query().Get("imdbId"))
	if imdbID == "" {
		imdbID, _ = record["imdbId"].(string)
	}
	updatedAt := sourceRecord["updatedAt"]
	if updatedAt == nil {
		updatedAt = record["updatedAt"]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"searchKey": searchKey, "tmdbId": parsedID, "imdbId": imdbID, "mediaType": mediaType,
		"seasonNumber": nullablePlayerNumber(season), "episodeNumber": nullablePlayerNumber(episode),
		"updatedAt": updatedAt, "scraping": false, "notAvailable": record["notAvailable"] == true,
		"downloads": downloads, "sources": sources,
	})
}

func asContentMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case bson.M:
		return map[string]any(typed)
	default:
		return map[string]any{}
	}
}

func contentStringList(value any) []string {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case bson.A:
		values = []any(typed)
	case []string:
		return typed
	default:
		return []string{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func sanitizePlayerProviderURLs(value any) []string {
	urls := contentStringList(value)
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		urls = []string{strings.TrimSpace(text)}
	}
	result := make([]string, 0, len(urls))
	seen := make(map[string]struct{})
	for _, raw := range urls {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		path := strings.ToLower(parsed.EscapedPath())
		if !regexp.MustCompile(`/((e|embed|v|svid|evid|file|files)/)`).MatchString(path) && !strings.HasSuffix(path, ".html") && parsed.RawQuery == "" && parsed.Fragment == "" {
			continue
		}
		if _, exists := seen[raw]; exists {
			continue
		}
		seen[raw] = struct{}{}
		result = append(result, raw)
	}
	return result
}

func nullablePlayerNumber(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (h *ContentHandler) GetPlayerFallback(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mediaType")))
	tmdbID, parseErr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("tmdbId")), 10, 64)
	if (mediaType != "movie" && mediaType != "series") || parseErr != nil || tmdbID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mediaType and tmdbId are required."})
		return
	}
	if h.watchmodeAPIKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Watchmode fallback is not configured."})
		return
	}
	result, err := h.resolveWatchmodeFallback(r, tmdbID, strings.TrimSpace(r.URL.Query().Get("imdbId")), mediaType)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to resolve player fallback", "details": err.Error()})
		return
	}
	if result == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No Watchmode fallback found for this title."})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *ContentHandler) ScrapeEmbed(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(strings.TrimSpace(r.URL.Query().Get("url")))
	if err != nil || target == nil || target.Hostname() == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing or invalid url"})
		return
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid url"})
		return
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid url"})
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid url"})
		return
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		writeJSON(w, response.StatusCode, map[string]string{"error": "Failed to fetch"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 5<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch"})
		return
	}
	iframeRE := regexp.MustCompile(`(?is)<iframe\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	match := iframeRE.FindSubmatch(body)
	if len(match) < 2 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Embed not found on page"})
		return
	}
	embed, err := url.Parse(html.UnescapeString(strings.TrimSpace(string(match[1]))))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Embed not found on page"})
		return
	}
	http.Redirect(w, r, target.ResolveReference(embed).String(), http.StatusFound)
}

func (h *ContentHandler) resolveWatchmodeFallback(r *http.Request, tmdbID int64, imdbID, mediaType string) (map[string]any, error) {
	searchValues := []string{imdbID, strconv.FormatInt(tmdbID, 10)}
	var titleID any
	for _, searchValue := range searchValues {
		if searchValue == "" {
			continue
		}
		endpoint := "https://api.watchmode.com/v1/search/?" + watchmodeQuery(h.watchmodeAPIKey, searchValue, "")
		payload, status, err := fetchWatchmodeJSON(r, endpoint)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			continue
		}
		results, _ := payload["title_results"].([]any)
		var firstTyped any
		for _, raw := range results {
			item, ok := raw.(map[string]any)
			if !ok || !watchmodeTypeMatches(item, mediaType) || item["id"] == nil {
				continue
			}
			if firstTyped == nil {
				firstTyped = item["id"]
			}
			itemTMDB := fmt.Sprint(item["tmdb_id"])
			if itemTMDB == "<nil>" {
				itemTMDB = fmt.Sprint(item["tmdbid"])
			}
			itemIMDB := strings.TrimSpace(fmt.Sprint(item["imdb_id"]))
			if (itemTMDB == strconv.FormatInt(tmdbID, 10)) || (imdbID != "" && itemIMDB == imdbID) {
				titleID = item["id"]
				break
			}
		}
		if titleID == nil {
			titleID = firstTyped
		}
		if titleID != nil {
			break
		}
	}
	if titleID == nil {
		return nil, nil
	}

	for _, region := range []string{"IN", "US", "GB", "CA", ""} {
		endpoint := "https://api.watchmode.com/v1/title/" + url.PathEscape(fmt.Sprint(titleID)) + "/sources/?" + watchmodeQuery(h.watchmodeAPIKey, "", region)
		payload, status, err := fetchWatchmodeArray(r, endpoint)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			continue
		}
		candidates := make([]map[string]any, 0)
		for _, source := range payload {
			candidate, ok := source.(map[string]any)
			if !ok {
				continue
			}
			candidateURL := firstNonEmptyString(candidate, "web_url", "url", "link", "deep_link", "ios_url", "android_url")
			if candidateURL == "" {
				continue
			}
			candidate["resolved_url"] = candidateURL
			candidates = append(candidates, candidate)
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return watchmodeSourceRank(candidates[i]["type"]) < watchmodeSourceRank(candidates[j]["type"])
		})
		if len(candidates) == 0 {
			continue
		}
		best := candidates[0]
		label := firstNonEmptyString(best, "name", "source_name")
		if label == "" {
			label = "Watchmode"
		}
		return map[string]any{
			"url": best["resolved_url"], "label": label, "embeddable": false,
			"message": "Watchmode fallback loaded.",
		}, nil
	}
	return nil, nil
}

func watchmodeQuery(apiKey, searchValue, region string) string {
	values := url.Values{"apiKey": []string{apiKey}}
	if searchValue != "" {
		values.Set("search_value", searchValue)
		values.Set("search_type", "2")
	}
	if region != "" {
		values.Set("regions", region)
	}
	return values.Encode()
}

func fetchWatchmodeJSON(r *http.Request, endpoint string) (map[string]any, int, error) {
	body, status, err := fetchWatchmode(r, endpoint)
	if err != nil || status < 200 || status >= 300 {
		return nil, status, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, err
	}
	return payload, status, nil
}

func fetchWatchmodeArray(r *http.Request, endpoint string) ([]any, int, error) {
	body, status, err := fetchWatchmode(r, endpoint)
	if err != nil || status < 200 || status >= 300 {
		return nil, status, err
	}
	var payload []any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, err
	}
	return payload, status, nil
}

func fetchWatchmode(r *http.Request, endpoint string) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, 0, err
		}
		request.Header.Set("Accept", "application/json")
		response, err := (&http.Client{Timeout: 12 * time.Second}).Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 5<<20))
			response.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else {
				return body, response.StatusCode, nil
			}
		} else {
			lastErr = err
		}
		if attempt == 0 {
			select {
			case <-r.Context().Done():
				return nil, 0, r.Context().Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return nil, 0, lastErr
}

func watchmodeTypeMatches(item map[string]any, mediaType string) bool {
	value := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["type"])))
	if value == "<nil>" || value == "" {
		value = strings.ToLower(strings.TrimSpace(fmt.Sprint(item["media_type"])))
	}
	if mediaType == "movie" {
		return value == "movie"
	}
	return value == "tv_series" || value == "series" || value == "tv"
}

func watchmodeSourceRank(raw any) int {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(raw))) {
	case "sub":
		return 0
	case "free":
		return 1
	case "buy":
		return 3
	case "rent":
		return 4
	default:
		return 2
	}
}

func firstNonEmptyString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := value[key].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}
	return ""
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
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}
