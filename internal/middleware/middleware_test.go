package middleware_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/middleware"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestIDMiddleware(t *testing.T) {
	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.GetRequestID(r.Context())
		assert.NotEmpty(t, reqID)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
}

func TestRecovererMiddleware(t *testing.T) {
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := middleware.Recoverer(discardLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated unhandled panic in route")
	}))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]string
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	assert.Equal(t, "Something went wrong!", body["error"])
}

func TestAuthMiddlewareMissingToken(t *testing.T) {
	secret := "test-secret"
	handler := middleware.AuthenticateToken(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	assert.Equal(t, "Access token required", body["error"])
}

func TestAuthMiddlewareValidToken(t *testing.T) {
	secret := "test-secret"
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId":   "user123",
		"username": "rakesh",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	require.NoError(t, err)

	var capturedUser *domain.UserClaims
	handler := middleware.AuthenticateToken(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = middleware.GetUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedUser)
	assert.Equal(t, "user123", capturedUser.UserID)
	assert.Equal(t, "rakesh", capturedUser.Username)
}

func TestRateLimiter(t *testing.T) {
	limiter := middleware.NewRateLimiter(2, time.Second)
	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 2 requests pass
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/limited", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	// Third request immediately hits 429
	req := httptest.NewRequest(http.MethodGet, "/api/limited", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}
