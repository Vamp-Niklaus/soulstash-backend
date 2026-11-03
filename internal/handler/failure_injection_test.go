package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"soulstash/internal/domain"
	"soulstash/internal/handler"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
)

type failingUserRepo struct {
	mockUserRepo
}

func (f *failingUserRepo) FindByID(ctx context.Context, id string) (*domain.User, error) {
	return nil, errors.New("database connection refused")
}

func TestFailureInjection_MalformedJSONPayload(t *testing.T) {
	mockRepo := newMockUserRepo()
	jwtSecret := "test-jwt-secret-key-at-least-32-chars-long"
	authSvc := service.NewAuthService(mockRepo, nil, jwtSecret)
	h := handler.NewAuthHandler(authSvc, jwtSecret)

	r := chi.NewRouter()
	r.Post("/api/auth/login", h.Login)

	// Send invalid JSON syntax
	malformedBody := []byte(`{"username": "testuser", "password": `)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(malformedBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for malformed JSON, got %d", w.Code)
	}
}

func TestFailureInjection_TMDBUpstreamError(t *testing.T) {
	mockTMDB := &mockTMDBClient{
		getFunc: func(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
			return nil, errors.New("upstream gateway timeout (504)")
		},
	}
	ratingsSvc := &mockRatingsService{}
	adultEngine := service.NewAdultDecisionEngine()

	h := handler.NewContentHandler(mockTMDB, ratingsSvc, adultEngine, nil, "testsecret")

	r := chi.NewRouter()
	r.Get("/movies/{id}", h.GetMovieDetail)

	req := httptest.NewRequest(http.MethodGet, "/movies/999999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found for upstream failure, got %d", w.Code)
	}
}

func TestFailureInjection_EmptyPayloadValidation(t *testing.T) {
	mockRepo := newMockUserRepo()
	jwtSecret := "test-jwt-secret-key-at-least-32-chars-long"
	authSvc := service.NewAuthService(mockRepo, nil, jwtSecret)
	h := handler.NewAuthHandler(authSvc, jwtSecret)

	r := chi.NewRouter()
	r.Post("/api/auth/login", h.Login)

	// Send empty JSON object
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for missing required credentials, got %d", w.Code)
	}
}
