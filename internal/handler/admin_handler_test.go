package handler_test

import (
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
)

func TestAdminHandler_UnauthorizedWithoutToken(t *testing.T) {
	jwtSecret := "adminsecret123"
	adminH := handler.NewAdminHandler(nil, nil, nil, nil, nil, jwtSecret)

	r := chi.NewRouter()
	adminH.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/adult-criteria", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", w.Code)
	}
}

func TestAdminHandler_ForbiddenForNonAdminUser(t *testing.T) {
	jwtSecret := "adminsecret123"
	mockRepo := newMockUserRepo()
	mockRepo.users["stduser"] = &domain.User{
		Username: "stduser",
		Admin:    false,
	}

	adultEngine := service.NewAdultDecisionEngine()
	adminH := handler.NewAdminHandler(mockRepo, adultEngine, nil, nil, nil, jwtSecret)

	r := chi.NewRouter()
	adminH.RegisterRoutes(r)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId":   "stduser",
		"username": "stduser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(jwtSecret))

	req := httptest.NewRequest(http.MethodGet, "/adult-criteria", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden, got %d", w.Code)
	}
}

func TestAdminHandler_GetDecisionTreeAuthorized(t *testing.T) {
	jwtSecret := "adminsecret123"
	mockRepo := newMockUserRepo()
	mockRepo.users["adminuser"] = &domain.User{
		Username: "adminuser",
		Admin:    true,
	}

	adultEngine := service.NewAdultDecisionEngine()
	adminH := handler.NewAdminHandler(mockRepo, adultEngine, nil, nil, nil, jwtSecret)

	r := chi.NewRouter()
	adminH.RegisterRoutes(r)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId":   "adminuser",
		"username": "adminuser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(jwtSecret))

	req := httptest.NewRequest(http.MethodGet, "/adult-criteria", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", w.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := res["tree"]; !ok {
		t.Errorf("expected tree field in response")
	}
}
