package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if !ok || len(genres) != 3 {
		t.Errorf("expected 3 deduplicated genres, got %d", len(genres))
	}
}
