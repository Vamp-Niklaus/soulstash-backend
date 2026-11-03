package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
		if u.Username == username {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error) {
	for _, u := range m.users {
		if u.Username == identifier || u.Email == identifier {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) Create(ctx context.Context, user *domain.User) error {
	for _, u := range m.users {
		if u.Username == user.Username {
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
