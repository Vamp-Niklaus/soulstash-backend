package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/service"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCollectionService_CRUD(t *testing.T) {
	user := &domain.User{
		ID:                bson.NewObjectID(),
		Username:          "moviefan",
		CollectionVersion: 1,
		Collections: []domain.UserCollection{
			{
				ID:          "coll-default-watched",
				Name:        "Watched",
				IsDeletable: false,
				IsPublic:    true,
				Movies:      []any{},
			},
		},
	}

	repo := newMockCollectionRepo(user)
	svc := service.NewCollectionService(repo)

	// 1. Get Collections
	colls, ver, err := svc.GetCollections(context.Background(), user.ID.Hex())
	if err != nil {
		t.Fatalf("unexpected error getting collections: %v", err)
	}
	if len(colls) != 1 || ver != 1 {
		t.Errorf("expected 1 collection version 1, got %d collections version %d", len(colls), ver)
	}

	// 2. Create Collection
	createdRes, err := svc.CreateCollection(context.Background(), user.ID.Hex(), "Sci-Fi Favorites", "Best sci-fi movies", false, nil)
	if err != nil {
		t.Fatalf("failed to create collection: %v", err)
	}
	if createdRes.CollectionVersion != 2 {
		t.Errorf("expected version 2, got %d", createdRes.CollectionVersion)
	}

	// 3. Add movie
	movieItem := map[string]any{
		"id":         155,
		"title":      "The Dark Knight",
		"addedAt":    time.Now().Format(time.RFC3339),
		"media_type": "Movie",
	}
	addRes, err := svc.AddMovie(context.Background(), user.ID.Hex(), "Sci-Fi Favorites", movieItem, nil)
	if err != nil {
		t.Fatalf("failed to add movie: %v", err)
	}
	if addRes.CollectionVersion != 2 { // mock version
		t.Logf("add movie result version: %d", addRes.CollectionVersion)
	}

	// 4. Remove movie
	removeRes, err := svc.RemoveMovie(context.Background(), user.ID.Hex(), "Sci-Fi Favorites", 155, nil)
	if err != nil {
		t.Fatalf("failed to remove movie: %v", err)
	}
	if removeRes.Message != "Item removed from collection" {
		t.Errorf("expected item removed message, got %s", removeRes.Message)
	}

	// 5. Delete default collection should fail
	_, err = svc.DeleteCollection(context.Background(), user.ID.Hex(), "coll-default-watched", nil)
	if err == nil {
		t.Errorf("expected error deleting non-deletable default collection, got nil")
	}

	// 6. Delete custom collection
	delResult, err := svc.DeleteCollection(context.Background(), user.ID.Hex(), "Sci-Fi Favorites", nil)
	if err != nil {
		t.Fatalf("failed to delete custom collection: %v", err)
	}
	if delResult.Message == "" {
		t.Errorf("expected non-empty delete message")
	}
}

func TestCollectionService_PublicCollectionVisibilityParity(t *testing.T) {
	publicUser := &domain.User{
		Username: "creator",
		Collections: []domain.UserCollection{
			{Name: "Published", IsPublished: true},
			{Name: "Legacy Public", Visibility: "public"},
			{Name: "Watched", IsPublic: false},
		},
	}
	svc := service.NewCollectionService(newMockCollectionRepo(publicUser))
	if _, err := svc.GetPublicCollection(context.Background(), "creator", "Published"); err != nil {
		t.Fatalf("expected published collection to be readable: %v", err)
	}
	if _, err := svc.GetPublicCollection(context.Background(), "creator", "Legacy Public"); err != nil {
		t.Fatalf("expected legacy public collection to be readable: %v", err)
	}
	_, err := svc.GetPublicCollection(context.Background(), "creator", "Watched")
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != 403 {
		t.Fatalf("expected a private collection to return 403, got %v", err)
	}
}

type stubRatingsService struct {
	batchCalls int
}

func (s *stubRatingsService) ResolveRating(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error) {
	imdb := 8.8
	va := 8.4
	return &domain.RatingRecord{
		TmdbID:          tmdbID,
		MediaType:       mediaType,
		ImdbID:          "tt0137523",
		ImdbRating:      &imdb,
		VoteAverage:     &va,
		LookupAttempted: true,
		UpdatedAt:       time.Now(),
	}, nil
}

func (s *stubRatingsService) GetStoredRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error) {
	return nil, domain.ErrNotFound
}

func (s *stubRatingsService) ListRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
	return nil, nil
}

func (s *stubRatingsService) BatchEnrich(ctx context.Context, items []map[string]any) []map[string]any {
	s.batchCalls++
	out := make([]map[string]any, len(items))
	for i, itm := range items {
		cp := make(map[string]any, len(itm)+4)
		for k, v := range itm {
			cp[k] = v
		}
		if _, ok := cp["tmdbID"]; !ok {
			if id, ok := cp["id"]; ok {
				cp["tmdbID"] = id
			} else if id, ok := cp["movieId"]; ok {
				cp["tmdbID"] = id
			}
		}
		cp["imdb_rating"] = 7.9
		cp["vote_average"] = 7.5
		cp["imdb_id"] = "tt9990001"
		cp["rating_lookup_attempted"] = true
		cp["rating_checked_at"] = time.Now().UTC().Format(time.RFC3339)
		out[i] = cp
	}
	return out
}

func (s *stubRatingsService) GetCounters(ctx context.Context) (*domain.AppMetrics, error) {
	return &domain.AppMetrics{}, nil
}

func TestCollectionService_AddMovieDrawerAndEnrichMetadata(t *testing.T) {
	user := &domain.User{
		ID:                bson.NewObjectID(),
		Username:          "collector",
		CollectionVersion: 3,
		Collections: []domain.UserCollection{
			{
				ID:          "Watchlist",
				Name:        "Watchlist",
				IsDeletable: false,
				Movies: []any{
					bson.D{
						{Key: "movieId", Value: int32(999)},
						{Key: "title", Value: "Unrated BSON Item"},
						{Key: "media_type", Value: "Movie"},
					},
				},
			},
		},
	}

	repo := newMockCollectionRepo(user)
	rSvc := &stubRatingsService{}
	svc := service.NewCollectionService(repo, rSvc)

	// 1. AddMovie with bottom-drawer payload (movieId without id/ratings) resolves & persists ratings
	res, err := svc.AddMovie(context.Background(), user.ID.Hex(), "Watchlist", map[string]any{
		"movieId":    550,
		"title":      "Fight Club",
		"media_type": "Movie",
	}, nil)
	if err != nil {
		t.Fatalf("AddMovie failed: %v", err)
	}
	added := res.Collections[0].Movies[1].(map[string]any)
	if added["id"] != 550 || added["imdb_rating"] != 8.8 || added["vote_average"] != 8.4 || added["imdb_id"] != "tt0137523" {
		t.Fatalf("expected resolved rating fields on added item, got %+v", added)
	}

	// 2. EnrichMetadata enriches legacy unrated BSON item and updates stored collection
	enrichRes, err := svc.EnrichMetadata(context.Background(), user.ID.Hex(), "Watchlist", []map[string]any{
		{"contentId": 999, "mediaType": "Movie"},
	})
	if err != nil {
		t.Fatalf("EnrichMetadata failed: %v", err)
	}
	if rSvc.batchCalls != 1 {
		t.Fatalf("expected 1 BatchEnrich call, got %d", rSvc.batchCalls)
	}
	legacy := enrichRes.Collections[0].Movies[0].(map[string]any)
	if legacy["imdb_rating"] != 7.9 || legacy["vote_average"] != 7.5 || legacy["rating_lookup_attempted"] != true {
		t.Fatalf("expected legacy collection item to be enriched, got %+v", legacy)
	}
}
