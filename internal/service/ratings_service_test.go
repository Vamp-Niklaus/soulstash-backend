package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"soulstash/internal/domain"
)

type mockRatingsRepo struct {
	mu      sync.RWMutex
	ratings map[string]*domain.RatingRecord
	metrics *domain.AppMetrics
}

func newMockRatingsRepo() *mockRatingsRepo {
	return &mockRatingsRepo{
		ratings: make(map[string]*domain.RatingRecord),
		metrics: &domain.AppMetrics{
			TmdbDetailFetchCount: 10,
			OmdbFetchCount:       5,
		},
	}
}

func (m *mockRatingsRepo) FindRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := fmt.Sprintf("%d_%s", tmdbID, mediaType)
	if r, ok := m.ratings[key]; ok {
		return r, nil
	}
	return nil, nil
}

func (m *mockRatingsRepo) FindRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
	items := make([]domain.RatingRecord, 0, len(m.ratings))
	for _, item := range m.ratings {
		if mediaType == "" || item.MediaType == mediaType {
			items = append(items, *item)
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (m *mockRatingsRepo) UpsertRating(ctx context.Context, rating *domain.RatingRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%d_%s", rating.TmdbID, rating.MediaType)
	m.ratings[key] = rating
	return nil
}

func (m *mockRatingsRepo) IncrementMetric(ctx context.Context, metricKey string) error {
	return nil
}

func (m *mockRatingsRepo) GetMetrics(ctx context.Context) (*domain.AppMetrics, error) {
	return m.metrics, nil
}

func TestRatingsService_ImdbSentinelFiltering(t *testing.T) {
	repo := newMockRatingsRepo()
	svc := NewRatingsService(repo, nil, "")

	// 1. Pre-populate cache with 10.0 sentinel (Node bug/quirk)
	sentinel := 10.0
	repo.ratings["101_Movie"] = &domain.RatingRecord{
		TmdbID:     101,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt101101",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now(),
	}

	res, err := svc.ResolveRating(context.Background(), 101, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ImdbRating != nil {
		t.Errorf("expected ImdbRating to be filtered to nil for 10.0 sentinel, got %v", *res.ImdbRating)
	}
	if res.Source != "cache" {
		t.Errorf("expected Source 'cache', got %v", res.Source)
	}

	// 2. Pre-populate cache with real rating 8.5
	realRating := 8.5
	repo.ratings["202_Movie"] = &domain.RatingRecord{
		TmdbID:     202,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt202202",
		ImdbRating: &realRating,
		UpdatedAt:  time.Now(),
	}

	res2, err := svc.ResolveRating(context.Background(), 202, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.ImdbRating == nil || *res2.ImdbRating != 8.5 {
		t.Errorf("expected ImdbRating 8.5, got %v", res2.ImdbRating)
	}
}

func TestRatingsService_GetStoredRatingUsesLocalRowAnd404ForMissing(t *testing.T) {
	repo := newMockRatingsRepo()
	repo.ratings["99_Series"] = &domain.RatingRecord{TmdbID: 99, MediaType: "Series", Type: "Series"}
	svc := NewRatingsService(repo, nil, "")
	row, err := svc.GetStoredRating(context.Background(), 99, "tv")
	if err != nil || row == nil || row.MediaType != "Series" {
		t.Fatalf("expected local Series rating row, got %#v, %v", row, err)
	}
	if _, err := svc.GetStoredRating(context.Background(), 100, "movie"); err == nil {
		t.Fatal("expected a missing rating row to return 404")
	} else if appErr, ok := err.(*domain.AppError); !ok || appErr.Code != 404 {
		t.Fatalf("expected 404 app error for missing row, got %v", err)
	}
}

func TestRatingsService_BatchEnrich(t *testing.T) {
	repo := newMockRatingsRepo()
	svc := NewRatingsService(repo, nil, "")

	items := []map[string]any{
		{"id": 101, "media_type": "Movie", "title": "Movie One"},
		{"id": 202, "media_type": "Series", "name": "Series Two"},
	}

	enriched := svc.BatchEnrich(context.Background(), items)
	if len(enriched) != 2 {
		t.Fatalf("expected 2 enriched items, got %d", len(enriched))
	}
}
