package service_test

import (
	"context"
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
