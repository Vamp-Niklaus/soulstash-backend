package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/handler"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// MockUserRepository implements repository.UserRepository for testing
type mockUserRepo struct {
	users map[string]*domain.User
}

type successfulEmailSender struct{}

func (successfulEmailSender) SendEmail(context.Context, string, string, string, string) error {
	return nil
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{users: make(map[string]*domain.User)}
}

func (m *mockUserRepo) FindByID(ctx context.Context, id string) (*domain.User, error) {
	for _, u := range m.users {
		if u.ID.Hex() == id || u.Username == id {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByUsername(ctx context.Context, username string) (*domain.User, error) {
	for _, u := range m.users {
		if strings.EqualFold(u.Username, strings.TrimSpace(username)) {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	for _, user := range m.users {
		if strings.EqualFold(user.Email, email) {
			return user, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error) {
	for _, u := range m.users {
		if strings.EqualFold(u.Username, strings.TrimSpace(identifier)) || strings.EqualFold(u.Email, strings.TrimSpace(identifier)) {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) Create(ctx context.Context, user *domain.User) error {
	for _, u := range m.users {
		if strings.EqualFold(u.Username, user.Username) || (user.Email != "" && strings.EqualFold(u.Email, user.Email)) {
			return domain.ErrConflict
		}
	}
	if user.ID.IsZero() {
		user.ID = bson.NewObjectID()
	}
	m.users[user.ID.Hex()] = user
	return nil
}

func (m *mockUserRepo) Update(ctx context.Context, id string, update bson.M) error {
	u, err := m.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if fn, ok := update["firstName"].(string); ok {
		u.FirstName = fn
	}
	return nil
}

func (m *mockUserRepo) SavePasswordReset(ctx context.Context, email, otp string, expiresAt time.Time) error {
	return nil
}

func (m *mockUserRepo) FindValidPasswordReset(ctx context.Context, email, otp string) (string, error) {
	return "dummy-reset-id", nil
}

func (m *mockUserRepo) DeletePasswordReset(ctx context.Context, resetID string) error {
	return nil
}

func (m *mockUserRepo) UpdatePassword(ctx context.Context, userID, hashedPassword string) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	u.Password = hashedPassword
	return nil
}

func (m *mockUserRepo) FindAllUsers(ctx context.Context) ([]domain.User, error) {
	var list []domain.User
	for _, u := range m.users {
		list = append(list, *u)
	}
	return list, nil
}

func (m *mockUserRepo) UpdatePreferences(ctx context.Context, userID string, showAdult bool, adminMode int) error {
	return nil
}

func (m *mockUserRepo) GetWatchProgress(ctx context.Context, userID string) ([]map[string]any, error) {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return []map[string]any{}, err
	}
	if u.WatchProgress == nil {
		return []map[string]any{}, nil
	}
	return u.WatchProgress, nil
}

func (m *mockUserRepo) SaveWatchProgress(ctx context.Context, userID string, item map[string]any) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	key, _ := item["key"].(string)
	seriesGroup, _ := item["seriesGroup"].(string)
	filtered := make([]map[string]any, 0, len(u.WatchProgress)+1)
	filtered = append(filtered, item)
	for _, existing := range u.WatchProgress {
		ek, _ := existing["key"].(string)
		eg, _ := existing["seriesGroup"].(string)
		if ek == key || (seriesGroup != "" && (eg == seriesGroup || strings.HasPrefix(ek, seriesGroup+"_s"))) {
			continue
		}
		filtered = append(filtered, existing)
	}
	u.WatchProgress = filtered
	return nil
}

func (m *mockUserRepo) DeleteWatchProgress(ctx context.Context, userID, key string) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	filtered := make([]map[string]any, 0, len(u.WatchProgress))
	for _, existing := range u.WatchProgress {
		ek, _ := existing["key"].(string)
		if ek == key {
			continue
		}
		filtered = append(filtered, existing)
	}
	u.WatchProgress = filtered
	return nil
}

func (m *mockUserRepo) ClearWatchProgress(ctx context.Context, userID string) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	u.WatchProgress = []map[string]any{}
	return nil
}

func (m *mockUserRepo) GetWatchedEpisodes(ctx context.Context, userID string) ([]string, error) {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return []string{}, err
	}
	if u.WatchedEpisodes == nil {
		return []string{}, nil
	}
	return u.WatchedEpisodes, nil
}

func (m *mockUserRepo) AddWatchedEpisodes(ctx context.Context, userID string, epKeys []string) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(u.WatchedEpisodes))
	for _, k := range u.WatchedEpisodes {
		seen[k] = true
	}
	for _, k := range epKeys {
		clean := strings.ToLower(strings.TrimSpace(k))
		if clean != "" && !seen[clean] {
			seen[clean] = true
			u.WatchedEpisodes = append(u.WatchedEpisodes, clean)
		}
	}
	return nil
}

func (m *mockUserRepo) RemoveWatchedEpisode(ctx context.Context, userID, epKey string) error {
	u, err := m.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	clean := strings.ToLower(strings.TrimSpace(epKey))
	next := make([]string, 0, len(u.WatchedEpisodes))
	for _, k := range u.WatchedEpisodes {
		if k != clean {
			next = append(next, k)
		}
	}
	u.WatchedEpisodes = next
	return nil
}

func setupTestAuthRouter() (chi.Router, *handler.AuthHandler, string) {
	secret := "test-jwt-secret-key-at-least-32-chars-long"
	repo := newMockUserRepo()
	authSvc := service.NewAuthService(repo, nil, secret)
	authHandler := handler.NewAuthHandler(authSvc, secret)

	r := chi.NewRouter()
	r.Route("/api/auth", authHandler.RegisterRoutes)
	r.Route("/api/user", authHandler.RegisterRoutes)

	return r, authHandler, secret
}

func TestCheckUsernameAvailableContract(t *testing.T) {
	r, _, _ := setupTestAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/auth/check-username?username=freshuser", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var actual map[string]any
	err := json.NewDecoder(rec.Body).Decode(&actual)
	require.NoError(t, err)

	goldenBytes, err := os.ReadFile("../../testdata/golden/auth/check_username_available.json")
	require.NoError(t, err)

	var expected map[string]any
	err = json.Unmarshal(goldenBytes, &expected)
	require.NoError(t, err)

	assert.Equal(t, expected["available"], actual["available"])
	assert.Equal(t, expected["message"], actual["message"])
}

func TestCheckUsernameShortContract(t *testing.T) {
	r, _, _ := setupTestAuthRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/auth/check-username?username=ab", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var actual map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&actual)

	goldenBytes, err := os.ReadFile("../../testdata/golden/auth/check_username_short.json")
	require.NoError(t, err)

	var expected map[string]any
	_ = json.Unmarshal(goldenBytes, &expected)

	assert.Equal(t, expected["available"], actual["available"])
	assert.Equal(t, expected["message"], actual["message"])
}

func TestRegisterAndLoginFlow(t *testing.T) {
	r, _, secret := setupTestAuthRouter()

	// 1. Validation failure: missing password
	badReqBody := `{"username":"alice","fullName":"Alice Wonder"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewBufferString(badReqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	assert.Equal(t, "Name, username and password are required", errResp["error"])

	// 2. Successful Registration
	validReg := `{"username":"alice","password":"password123","fullName":"Alice Wonder"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewBufferString(validReg))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	var regResp service.AuthResult
	err := json.NewDecoder(rec.Body).Decode(&regResp)
	require.NoError(t, err)
	assert.NotEmpty(t, regResp.Token)
	assert.Equal(t, "alice", regResp.User.Username)
	assert.Equal(t, "Alice Wonder", regResp.User.FullName)

	// 3. Login with correct credentials
	loginBody := `{"username":"alice","password":"password123"}`
	req = httptest.NewRequest(http.MethodPost, "/api/user/login", bytes.NewBufferString(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var loginResp service.AuthResult
	_ = json.NewDecoder(rec.Body).Decode(&loginResp)
	assert.NotEmpty(t, loginResp.Token)

	// 4. Verify token endpoint with the login token
	req = httptest.NewRequest(http.MethodPost, "/api/auth/verify-token", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.Token)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var verifyResp service.TokenVerifyResult
	_ = json.NewDecoder(rec.Body).Decode(&verifyResp)
	assert.True(t, verifyResp.Success)
	assert.Equal(t, "alice", verifyResp.Username)

	// 5. Logout endpoint
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	_ = secret
}

func TestSendOTPDoesNotExposeDevelopmentCode(t *testing.T) {
	secret := "test-jwt-secret-key-at-least-32-chars-long"
	service := service.NewAuthService(newMockUserRepo(), successfulEmailSender{}, secret)
	h := handler.NewAuthHandler(service, secret)
	r := chi.NewRouter()
	r.Post("/api/auth/send-otp", h.SendOTP)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/send-otp", bytes.NewBufferString(`{"email":"user@example.com","username":"newuser","fullName":"New User"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Environment", "development")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	assert.Equal(t, "OTP sent successfully", payload["message"])
	assert.NotContains(t, payload, "otp")
}

func TestWatchProgressAndWatchedEpisodesSync(t *testing.T) {
	r, _, _ := setupTestAuthRouter()

	// Register a user to obtain a valid JWT
	regReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewBufferString(`{"username":"bob","password":"password123","fullName":"Bob Builder"}`))
	regReq.Header.Set("Content-Type", "application/json")
	regRec := httptest.NewRecorder()
	r.ServeHTTP(regRec, regReq)
	require.Equal(t, http.StatusCreated, regRec.Code)

	var regResp service.AuthResult
	require.NoError(t, json.NewDecoder(regRec.Body).Decode(&regResp))
	token := regResp.Token

	// Save watched episode S1E3
	weReq := httptest.NewRequest(http.MethodPost, "/api/auth/watched-episodes", bytes.NewBufferString(`{"keys":["1399_s1_e3"]}`))
	weReq.Header.Set("Content-Type", "application/json")
	weReq.Header.Set("Authorization", "Bearer "+token)
	weRec := httptest.NewRecorder()
	r.ServeHTTP(weRec, weReq)
	require.Equal(t, http.StatusOK, weRec.Code)

	// Save continue watching next episode S1E4
	cwReq := httptest.NewRequest(http.MethodPost, "/api/auth/watch-progress", bytes.NewBufferString(`{"key":"1399_s1_e4","seriesGroup":"1399","id":1399,"seasonNumber":1,"episodeNumber":4,"isNextEpisode":true}`))
	cwReq.Header.Set("Content-Type", "application/json")
	cwReq.Header.Set("Authorization", "Bearer "+token)
	cwRec := httptest.NewRecorder()
	r.ServeHTTP(cwRec, cwReq)
	require.Equal(t, http.StatusOK, cwRec.Code)

	// Clear continue watching should NOT clear watched episodes
	clearReq := httptest.NewRequest(http.MethodDelete, "/api/auth/watch-progress", nil)
	clearReq.Header.Set("Authorization", "Bearer "+token)
	clearRec := httptest.NewRecorder()
	r.ServeHTTP(clearRec, clearReq)
	require.Equal(t, http.StatusOK, clearRec.Code)

	// Verify continue watching is empty
	getCwReq := httptest.NewRequest(http.MethodGet, "/api/auth/watch-progress", nil)
	getCwReq.Header.Set("Authorization", "Bearer "+token)
	getCwRec := httptest.NewRecorder()
	r.ServeHTTP(getCwRec, getCwReq)
	require.Equal(t, http.StatusOK, getCwRec.Code)
	var cwList []map[string]any
	require.NoError(t, json.NewDecoder(getCwRec.Body).Decode(&cwList))
	assert.Len(t, cwList, 0)

	// Verify watched episodes remain intact in DB
	getWeReq := httptest.NewRequest(http.MethodGet, "/api/auth/watched-episodes", nil)
	getWeReq.Header.Set("Authorization", "Bearer "+token)
	getWeRec := httptest.NewRecorder()
	r.ServeHTTP(getWeRec, getWeReq)
	require.Equal(t, http.StatusOK, getWeRec.Code)
	var weResp struct {
		Episodes []string `json:"episodes"`
	}
	require.NoError(t, json.NewDecoder(getWeRec.Body).Decode(&weResp))
	assert.Equal(t, []string{"1399_s1_e3"}, weResp.Episodes)

	// Remove watched episode manually
	delWeReq := httptest.NewRequest(http.MethodDelete, "/api/auth/watched-episodes/1399_s1_e3", nil)
	delWeReq.Header.Set("Authorization", "Bearer "+token)
	delWeRec := httptest.NewRecorder()
	r.ServeHTTP(delWeRec, delWeReq)
	require.Equal(t, http.StatusOK, delWeRec.Code)
}
