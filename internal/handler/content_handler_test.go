package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"soulstash/internal/domain"
	"soulstash/internal/handler"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
)

type mockTMDBClient struct {
	getFunc    func(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error)
	getRawFunc func(ctx context.Context, endpoint string, queryParams map[string]string) ([]byte, error)
}

func (m *mockTMDBClient) Get(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, endpoint, queryParams)
	}
	return map[string]any{}, nil
}

func (m *mockTMDBClient) GetRaw(ctx context.Context, endpoint string, queryParams map[string]string) ([]byte, error) {
	if m.getRawFunc != nil {
		return m.getRawFunc(ctx, endpoint, queryParams)
	}
	return []byte(`{"results":[]}`), nil
}

type mockRatingsService struct {
	resolveFunc func(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error)
	listFunc    func(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error)
}

func (m *mockRatingsService) ResolveRating(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, tmdbID, mediaType, seedImdbID, seedVoteAvg)
	}
	val := 8.5
	return &domain.RatingRecord{
		TmdbID:     tmdbID,
		MediaType:  mediaType,
		ImdbRating: &val,
	}, nil
}

func (m *mockRatingsService) BatchEnrich(ctx context.Context, items []map[string]any) []map[string]any {
	return items
}

func (m *mockRatingsService) ListRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, mediaType, limit)
	}
	return []domain.RatingRecord{}, nil
}

func (m *mockRatingsService) GetStoredRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error) {
	return &domain.RatingRecord{TmdbID: tmdbID, MediaType: mediaType}, nil
}

func (m *mockRatingsService) GetCounters(ctx context.Context) (*domain.AppMetrics, error) {
	return &domain.AppMetrics{}, nil
}

func TestContentHandler_GetMovieDetail(t *testing.T) {
	mockTMDB := &mockTMDBClient{
		getFunc: func(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
			return map[string]any{
				"id":           550,
				"title":        "Fight Club",
				"imdb_id":      "tt0137523",
				"vote_average": 8.4,
			}, nil
		},
	}

	ratingsSvc := &mockRatingsService{}
	adultEngine := service.NewAdultDecisionEngine()

	h := handler.NewContentHandler(mockTMDB, ratingsSvc, adultEngine, nil, "testsecret")

	r := chi.NewRouter()
	r.Get("/movies/{id}", h.GetMovieDetail)

	req := httptest.NewRequest(http.MethodGet, "/movies/550", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res["title"] != "Fight Club" {
		t.Errorf("expected title Fight Club, got %v", res["title"])
	}
	if res["imdb_rating"] != 8.5 {
		t.Errorf("expected imdb_rating 8.5, got %v", res["imdb_rating"])
	}
}

func TestContentHandler_GetGenresCombined(t *testing.T) {
	mockTMDB := &mockTMDBClient{
		getFunc: func(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
			if endpoint == "/3/genre/movie/list" {
				return map[string]any{
					"genres": []any{
						map[string]any{"id": float64(28), "name": "Action"},
						map[string]any{"id": float64(12), "name": "Adventure"},
					},
				}, nil
			}
			return map[string]any{
				"genres": []any{
					map[string]any{"id": float64(28), "name": "Action"}, // duplicate
					map[string]any{"id": float64(18), "name": "Drama"},
				},
			}, nil
		},
	}

	ratingsSvc := &mockRatingsService{}
	adultEngine := service.NewAdultDecisionEngine()
	h := handler.NewContentHandler(mockTMDB, ratingsSvc, adultEngine, nil, "testsecret")

	r := chi.NewRouter()
	r.Get("/api/genres", h.GetGenres)

	req := httptest.NewRequest(http.MethodGet, "/api/genres", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	genres, ok := res["genres"].([]any)
	if !ok || len(genres) != 4 {
		t.Errorf("expected 4 genres including the Bollywood entry, got %d", len(genres))
	}
}

func TestContentHandler_QueryRatingsReturnsSortedTableEnvelope(t *testing.T) {
	var gotMediaType string
	var gotLimit int
	ratingsSvc := &mockRatingsService{
		listFunc: func(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
			gotMediaType, gotLimit = mediaType, limit
			return []domain.RatingRecord{{TmdbID: 7, MediaType: "Series"}}, nil
		},
	}
	h := handler.NewContentHandler(&mockTMDBClient{}, ratingsSvc, service.NewAdultDecisionEngine(), nil, "testsecret")
	r := chi.NewRouter()
	r.Get("/ratings", h.QueryRatings)
	req := httptest.NewRequest(http.MethodGet, "/ratings?mediaType=tv&limit=20000", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotMediaType != "tv" || gotLimit != 10000 {
		t.Fatalf("expected normalized service query and capped limit, got type=%q limit=%d", gotMediaType, gotLimit)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected ratings table envelope, got %#v", payload)
	}
}

func TestContentHandler_SearchUsesNodeBatchShape(t *testing.T) {
	mockTMDB := &mockTMDBClient{
		getFunc: func(ctx context.Context, endpoint string, params map[string]string) (map[string]any, error) {
			if endpoint == "/3/search/movie" && params["page"] == "1" {
				return map[string]any{
					"total_pages": float64(4),
					"results": []any{map[string]any{
						"id": float64(438631), "title": "Dune", "original_title": "Dune",
						"poster_path": "/dune.jpg", "release_date": "2021-09-15",
						"original_language": "en", "origin_country": []any{"US"},
						"vote_count": float64(1000), "vote_average": float64(8.0),
					}},
				}, nil
			}
			return map[string]any{"total_pages": float64(4), "results": []any{}}, nil
		},
	}
	h := handler.NewContentHandler(mockTMDB, &mockRatingsService{}, service.NewAdultDecisionEngine(), nil, "testsecret")
	r := chi.NewRouter()
	r.Get("/search", h.Search)
	req := httptest.NewRequest(http.MethodGet, "/search?q=Dune&limit=20&page=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("expected one shaped result, got %v", payload["results"])
	}
	result := results[0].(map[string]any)
	if result["media_type"] != "Movie" || result["title"] != "Dune" {
		t.Fatalf("unexpected search result shape: %#v", result)
	}
	if payload["page"] != float64(1) || payload["hasMore"] != true {
		t.Fatalf("expected Node batch pagination, got %#v", payload)
	}
	if w.Header().Get("Cache-Control") == "" {
		t.Fatal("expected no-cache headers for search responses")
	}
}

func TestContentHandler_GetPlayerSourcesUsesTMDBVideoEndpoint(t *testing.T) {
	var gotEndpoint string
	mockTMDB := &mockTMDBClient{
		getRawFunc: func(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
			gotEndpoint = endpoint
			return []byte(`{"results":[{"key":"official-trailer","site":"YouTube","type":"Trailer"}]}`), nil
		},
	}
	h := handler.NewContentHandler(mockTMDB, &mockRatingsService{}, service.NewAdultDecisionEngine(), nil, "testsecret")
	r := chi.NewRouter()
	r.Get("/player/sources", h.GetPlayerSources)
	req := httptest.NewRequest(http.MethodGet, "/player/sources?mediaType=movie&tmdbId=42", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if gotEndpoint != "/3/movie/42/videos" {
		t.Fatalf("expected TMDB video endpoint with /3 prefix, got %q", gotEndpoint)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	sources, ok := payload["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("expected a YouTube source, got %#v", payload["sources"])
	}
	url := sources[0].(map[string]any)["url"].(string)
	if !strings.Contains(url, "official-trailer") {
		t.Fatalf("expected the YouTube embed URL, got %q", url)
	}
}

func TestContentHandler_DiscoverBollywoodUsesFourMonthWindow(t *testing.T) {
	var mu sync.Mutex
	var movieParams map[string]string
	mockTMDB := &mockTMDBClient{
		getFunc: func(ctx context.Context, endpoint string, params map[string]string) (map[string]any, error) {
			if endpoint == "/3/discover/movie" {
				mu.Lock()
				movieParams = params
				mu.Unlock()
				results := make([]any, 15)
				for i := range results {
					results[i] = map[string]any{
						"id": float64(i + 1), "title": "Film", "adult": false,
						"poster_path":  "/film.jpg",
						"release_date": "2026-10-01", "original_language": "hi",
						"origin_country": []any{"IN"}, "popularity": float64(100 - i),
					}
				}
				return map[string]any{"total_pages": float64(1), "results": results}, nil
			}
			return map[string]any{"total_pages": float64(1), "results": []any{}}, nil
		},
	}
	h := handler.NewContentHandler(mockTMDB, &mockRatingsService{}, service.NewAdultDecisionEngine(), nil, "testsecret")
	r := chi.NewRouter()
	r.Get("/movies", h.DiscoverMovies)
	req := httptest.NewRequest(http.MethodGet, "/movies?genre=bollywood&page=4&limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	mu.Lock()
	params := movieParams
	mu.Unlock()
	if params["primary_release_date.gte"] == "" || params["primary_release_date.lte"] == "" {
		t.Fatalf("expected 4-month release window filters, got %#v", params)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	results := payload["movies"].([]any)
	if len(results) != 10 {
		t.Fatalf("expected page to contain 10 Bollywood items, got %d", len(results))
	}
	result := results[0].(map[string]any)
	if result["is_bollywood_endpoint"] != true || result["media_type"] != "Movie" {
		t.Fatalf("expected Bollywood metadata on result, got %#v", result)
	}
}

func TestContentHandler_DiscoverFiltersAnimeUsingMovieProductionCountries(t *testing.T) {
	mockTMDB := &mockTMDBClient{
		getFunc: func(_ context.Context, endpoint string, _ map[string]string) (map[string]any, error) {
			if strings.Contains(endpoint, "/movie") {
				return map[string]any{"total_pages": float64(1), "results": []any{
					map[string]any{"id": float64(1), "title": "Anime", "poster_path": "/anime.jpg", "original_language": "ja", "genre_ids": []any{float64(16)}, "production_countries": []any{map[string]any{"iso_3166_1": "JP"}}},
					map[string]any{"id": float64(2), "title": "Animation", "poster_path": "/animation.jpg", "original_language": "en", "genre_ids": []any{float64(16)}, "production_countries": []any{map[string]any{"iso_3166_1": "US"}}},
				}}, nil
			}
			return map[string]any{"total_pages": float64(1), "results": []any{}}, nil
		},
	}
	h := handler.NewContentHandler(mockTMDB, &mockRatingsService{}, service.NewAdultDecisionEngine(), nil, "testsecret")
	r := chi.NewRouter()
	r.Get("/movies", h.DiscoverMovies)
	req := httptest.NewRequest(http.MethodGet, "/movies?genre=35", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode discover response: %v", err)
	}
	results := payload["movies"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["title"] != "Animation" {
		t.Fatalf("expected only non-Japanese animation after filtering, got %#v", results)
	}
}
