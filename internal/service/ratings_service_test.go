package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

type mockTmdbForRatings struct {
	calls atomic.Int32
	data  map[string]map[string]any
}

func (m *mockTmdbForRatings) Get(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
	m.calls.Add(1)
	if d, ok := m.data[endpoint]; ok {
		return d, nil
	}
	return nil, nil
}

func (m *mockTmdbForRatings) GetRaw(ctx context.Context, endpoint string, queryParams map[string]string) ([]byte, error) {
	return nil, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestRatingsService_SentinelEmptyImdbID_And_OmdbExpiryFlow(t *testing.T) {
	repo := newMockRatingsRepo()
	tmdbMock := &mockTmdbForRatings{
		data: map[string]map[string]any{
			"/3/movie/402": {"imdb_id": "tt0000402", "vote_average": 6.8},
			"/3/movie/403": {"imdb_id": "tt0000403", "vote_average": 7.9},
			"/3/tv/555":    {"imdb_id": "tt0000555", "vote_average": 8.1},
		},
	}

	var omdbCalls atomic.Int32
	svc := NewRatingsService(repo, tmdbMock, "key1").(*ratingsService)
	svc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			omdbCalls.Add(1)
			q := req.URL.Query().Get("i")
			body := `{"Response":"False","Error":"Movie not found!"}`
			if q == "tt0000403" || q == "tt0000555" {
				body = `{"Response":"True","imdbRating":"8.3"}`
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	// 1. Fresh 10.0 sentinel WITH empty ImdbID must still return from cache without hitting TMDB/OMDb
	sentinel := InvalidImdbSentinel
	repo.ratings["401_Movie"] = &domain.RatingRecord{
		TmdbID:     401,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now().Add(-2 * 24 * time.Hour),
	}
	res1, err := svc.ResolveRating(context.Background(), 401, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.Source != "cache" || res1.ImdbRating != nil || tmdbMock.calls.Load() != 0 || omdbCalls.Load() != 0 {
		t.Fatalf("expected cache hit with 0 external calls, got source=%s tmdb=%d omdb=%d", res1.Source, tmdbMock.calls.Load(), omdbCalls.Load())
	}

	// 2. Expired 10.0 sentinel where OMDb still says not found -> calls OMDb once, updates MongoDB UpdatedAt to now, then subsequent check skips OMDb
	repo.ratings["402_Movie"] = &domain.RatingRecord{
		TmdbID:     402,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now().Add(-20 * 24 * time.Hour),
	}
	res2, err := svc.ResolveRating(context.Background(), 402, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if omdbCalls.Load() != 1 || res2.ImdbRating != nil {
		t.Fatalf("expected 1 omdb call and nil rating, got omdb=%d rating=%v", omdbCalls.Load(), res2.ImdbRating)
	}
	stored402 := repo.ratings["402_Movie"]
	if stored402 == nil || stored402.ImdbRating == nil || *stored402.ImdbRating != InvalidImdbSentinel || time.Since(stored402.UpdatedAt) > time.Minute {
		t.Fatalf("expected fresh 10.0 sentinel stored in MongoDB, got %+v", stored402)
	}

	// Repeat check immediately: must hit fresh 10.0 in MongoDB and NOT call OMDb again
	res2Again, err := svc.ResolveRating(context.Background(), 402, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2Again.Source != "cache" || omdbCalls.Load() != 1 {
		t.Fatalf("expected repeat lookup to serve from cache without OMDb call, got source=%s omdb=%d", res2Again.Source, omdbCalls.Load())
	}

	// 3. Expired 10.0 sentinel where OMDb now finds a valid rating -> updates MongoDB with found rating
	repo.ratings["403_Movie"] = &domain.RatingRecord{
		TmdbID:     403,
		MediaType:  "Movie",
		Type:       "Movie",
		ImdbID:     "tt0000403",
		ImdbRating: &sentinel,
		UpdatedAt:  time.Now().Add(-15 * 24 * time.Hour),
	}
	res3, err := svc.ResolveRating(context.Background(), 403, "Movie", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.ImdbRating == nil || *res3.ImdbRating != 8.3 {
		t.Fatalf("expected 8.3 rating after OMDb lookup, got %v", res3.ImdbRating)
	}
	if repo.ratings["403_Movie"].ImdbRating == nil || *repo.ratings["403_Movie"].ImdbRating != 8.3 {
		t.Fatalf("expected MongoDB cache updated to 8.3")
	}

	// 4. BatchEnrich supports seriesId / movieId drawer payloads
	enriched := svc.BatchEnrich(context.Background(), []map[string]any{
		{"seriesId": 555, "title": "Show 555"},
	})
	if len(enriched) != 1 || enriched[0]["mediaType"] != "Series" || enriched[0]["tmdbID"] != 555 {
		t.Fatalf("unexpected BatchEnrich result: %+v", enriched)
	}
	if r, ok := enriched[0]["imdb_rating"].(*float64); !ok || r == nil || *r != 8.3 {
		t.Fatalf("expected BatchEnrich imdb_rating 8.3, got %#v", enriched[0]["imdb_rating"])
	}
}
