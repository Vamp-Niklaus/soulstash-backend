package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/handler"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type mockCollectionRepo struct {
	user *domain.User
}

func (m *mockCollectionRepo) GetUser(ctx context.Context, userIDOrUsername string) (*domain.User, error) {
	if m.user == nil {
		return nil, domain.ErrNotFound
	}
	return m.user, nil
}

func (m *mockCollectionRepo) UpdateCollections(ctx context.Context, userID string, collections []domain.UserCollection) error {
	m.user.Collections = collections
	return nil
}

func (m *mockCollectionRepo) IncrementVersion(ctx context.Context, userID string) (int, error) {
	m.user.CollectionVersion++
	return m.user.CollectionVersion, nil
}

func (m *mockCollectionRepo) FollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	m.user.Following = append(m.user.Following, targetUsername)
	return nil
}

func (m *mockCollectionRepo) UnfollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	return nil
}

func (m *mockCollectionRepo) GetFollowers(ctx context.Context, username string) ([]string, error) {
	return []string{"alice"}, nil
}

func (m *mockCollectionRepo) GetFollowing(ctx context.Context, username string) ([]string, error) {
	return []string{"bob"}, nil
}

func (m *mockCollectionRepo) AddFavoritePerson(ctx context.Context, userID string, person any) error {
	m.user.FavoritePeople = append(m.user.FavoritePeople, person)
	return nil
}

func (m *mockCollectionRepo) RemoveFavoritePerson(ctx context.Context, userID string, personID int) error {
	return nil
}

func (m *mockCollectionRepo) SetFavoritePrivacy(ctx context.Context, userID string, isPublic bool) error {
	m.user.FavoritePeoplePublic = isPublic
	return nil
}

func (m *mockCollectionRepo) GetPublishedCollections(ctx context.Context) ([]map[string]any, error) {
	return []map[string]any{
		{"username": "testuser", "collection": map[string]any{"name": "Best Sci-Fi"}},
	}, nil
}

func setupTestCollectionRouter() (chi.Router, string, string) {
	secret := "test-jwt-secret-key-at-least-32-chars-long"
	userID := bson.NewObjectID().Hex()

	user := &domain.User{
		Username:          "testuser",
		CollectionVersion: 1,
		Collections: []domain.UserCollection{
			{Name: "Watched", IsDeletable: false, IsPublic: false, MovieCount: 0, Movies: []any{}},
			{Name: "Watchlist", IsDeletable: false, IsPublic: false, MovieCount: 0, Movies: []any{}},
		},
	}
	user.ID, _ = bson.ObjectIDFromHex(userID)

	repo := &mockCollectionRepo{user: user}
	colSvc := service.NewCollectionService(repo)
	colHandler := handler.NewCollectionHandler(colSvc, secret)

	r := chi.NewRouter()
	r.Route("/api", colHandler.RegisterRoutes)

	// Generate valid test JWT token
	claims := jwt.MapClaims{
		"userId":   userID,
		"username": "testuser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(secret))

	return r, tokenStr, userID
}

func TestGetUserCollections(t *testing.T) {
	r, token, _ := setupTestCollectionRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/user/collections", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("X-Collection-Version"))

	var cols []domain.UserCollection
	err := json.NewDecoder(rec.Body).Decode(&cols)
	require.NoError(t, err)
	assert.Len(t, cols, 2)
	assert.Equal(t, "Watched", cols[0].Name)
	assert.Equal(t, "Watchlist", cols[1].Name)
}

func TestCreateUserCollection(t *testing.T) {
	r, token, _ := setupTestCollectionRouter()

	createBody := `{"name":"Nolan Films","description":"All Christopher Nolan movies","isPublic":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/user/collections", bytes.NewBufferString(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "2", rec.Header().Get("X-Collection-Version"))

	var res service.CollectionMutationResult
	err := json.NewDecoder(rec.Body).Decode(&res)
	require.NoError(t, err)
	assert.Equal(t, 2, res.CollectionVersion)
	assert.Len(t, res.Collections, 3)
}

func TestPreventDeletingDefaultCollection(t *testing.T) {
	r, token, _ := setupTestCollectionRouter()

	req := httptest.NewRequest(http.MethodDelete, "/api/user/collections/Watched", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	assert.Equal(t, "Default collections cannot be deleted", errResp["error"])
}
