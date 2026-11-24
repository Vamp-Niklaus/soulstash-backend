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

func TestRatingsService_RoundRobinKeys(t *testing.T) {
	repo := newMockRatingsRepo()
	keys := []string{"keyA", "keyB", "keyC"}
	svc := NewRatingsService(repo, nil, keys...).(*ratingsService)

	if len(svc.omdbKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(svc.omdbKeys))
	}

	expected := []string{"keyA", "keyB", "keyC", "keyA", "keyB", "keyC"}
	for i, exp := range expected {
		k := svc.getNextOMDBKey()
		if k != exp {
			t.Errorf("iteration %d: expected key %s, got %s", i, exp, k)
		}
	}
}

func TestRatingsService_CacheExpiration(t *testing.T) {
	repo := newMockRatingsRepo()
	svc := NewRatingsService(repo, nil, "")

	// 1. Valid rating updated 10 days ago (fresh, within 30 days)
	rating85 := 8.5
	repo.ratings["301_Movie"] = &domain.RatingRecord{
		TmdbID:     301,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt301",
		ImdbRating: &rating85,
		UpdatedAt:  time.Now().Add(-10 * 24 * time.Hour),
	}
	res1, err := svc.ResolveRating(context.Background(), 301, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.Source != "cache" {
		t.Errorf("expected fresh rating to be served from cache, got source %s", res1.Source)
	}

	// 2. Valid rating updated 35 days ago (expired, > 30 days)
	repo.ratings["302_Movie"] = &domain.RatingRecord{
		TmdbID:     302,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt302",
		ImdbRating: &rating85,
		UpdatedAt:  time.Now().Add(-35 * 24 * time.Hour),
	}
	res2, err := svc.ResolveRating(context.Background(), 302, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Source == "cache" {
		t.Errorf("expected expired rating to trigger lookup, got cache")
	}

	// 3. Sentinel rating updated 5 days ago (fresh, within 14 days)
	sentinel := InvalidImdbSentinel
	repo.ratings["303_Movie"] = &domain.RatingRecord{
		TmdbID:     303,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt303",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now().Add(-5 * 24 * time.Hour),
	}
	res3, err := svc.ResolveRating(context.Background(), 303, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.Source != "cache" {
		t.Errorf("expected fresh sentinel to be served from cache, got source %s", res3.Source)
	}
	if res3.ImdbRating != nil {
		t.Errorf("expected sentinel rating to be filtered to nil, got %v", *res3.ImdbRating)
	}

	// 4. Sentinel rating updated 20 days ago (expired, > 14 days)
	repo.ratings["304_Movie"] = &domain.RatingRecord{
		TmdbID:     304,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt304",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now().Add(-20 * 24 * time.Hour),
	}
	res4, err := svc.ResolveRating(context.Background(), 304, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res4.Source == "cache" {
		t.Errorf("expected expired sentinel to trigger lookup, got cache")
	}

	// 5. GetStoredRating with expired record resolves and returns filtered sentinel
	stored, err := svc.GetStoredRating(context.Background(), 304, "Movie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.ImdbRating != nil {
		t.Errorf("expected GetStoredRating sentinel to be filtered to nil, got %v", *stored.ImdbRating)
	}
}
