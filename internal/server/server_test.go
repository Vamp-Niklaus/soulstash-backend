package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"soulstash/internal/config"
	"soulstash/internal/server"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer() *server.Server {
	cfg := &config.Config{
		Port:         3000,
		AppEnv:       "development",
		IsProduction: false,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return server.New(cfg, logger, nil, nil)
}

func TestHealthCheckEndpoint(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)

	assert.Equal(t, "OK", body["status"])
	assert.NotEmpty(t, body["timestamp"])
}

func TestReadinessEndpoint(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/api/ready", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	assert.Equal(t, "READY", body["status"])
}

func TestImageProxyEndpoint(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/image/sample-poster.jpg", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)

	assert.Equal(t, "https://image.tmdb.org/t/p/w500/sample-poster.jpg", body["imageUrl"])
}

func TestNotFoundHandler(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/non-existent-random-route", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var body map[string]string
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	assert.Equal(t, "Route not found", body["error"])
}

func TestTopLevelHealthAndCronEndpoints(t *testing.T) {
	srv := newTestServer()

	// 1. Top-level /health
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	recHealth := httptest.NewRecorder()
	srv.Router().ServeHTTP(recHealth, reqHealth)
	assert.Equal(t, http.StatusOK, recHealth.Code)

	var healthBody map[string]any
	err := json.NewDecoder(recHealth.Body).Decode(&healthBody)
	require.NoError(t, err)
	assert.Equal(t, "OK", healthBody["status"])

	// 2. Cron keep-alive JSON response
	reqCronJSON := httptest.NewRequest(http.MethodGet, "/cron/health", nil)
	reqCronJSON.Header.Set("Accept", "application/json")
	recCronJSON := httptest.NewRecorder()
	srv.Router().ServeHTTP(recCronJSON, reqCronJSON)
	assert.Equal(t, http.StatusOK, recCronJSON.Code)

	var cronBody map[string]any
	err = json.NewDecoder(recCronJSON.Body).Decode(&cronBody)
	require.NoError(t, err)
	assert.Equal(t, "healthy", cronBody["status"])
	assert.Equal(t, true, cronBody["render_keep_alive"])

	// 3. Cron keep-alive HTML response (browser visit)
	reqCronHTML := httptest.NewRequest(http.MethodGet, "/cron-health", nil)
	reqCronHTML.Header.Set("Accept", "text/html,application/xhtml+xml")
	recCronHTML := httptest.NewRecorder()
	srv.Router().ServeHTTP(recCronHTML, reqCronHTML)
	assert.Equal(t, http.StatusOK, recCronHTML.Code)
	assert.Contains(t, recCronHTML.Body.String(), "Soulstash Backend")
	assert.Contains(t, recCronHTML.Body.String(), "LIVE & HEALTHY")
}
