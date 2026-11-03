package service_test

import (
	"context"
	"sync"
	"time"

	"soulstash/internal/domain"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type mockUserRepo struct {
	mu    sync.Mutex
	users map[string]*domain.User
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		users: make(map[string]*domain.User),
	}
}

func (m *mockUserRepo) FindByID(ctx context.Context, id string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID.Hex() == id || u.Username == id {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByUsername(ctx context.Context, username string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Username == identifier || u.Email == identifier {
			return u, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (m *mockUserRepo) Create(ctx context.Context, user *domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID.Hex() == id || u.Username == id {
			if fn, ok := update["firstName"].(string); ok {
				u.FirstName = fn
			}
			return nil
		}
	}
	return domain.ErrNotFound
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID.Hex() == userID || u.Username == userID {
			u.Password = hashedPassword
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *mockUserRepo) FindAllUsers(ctx context.Context) ([]domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []domain.User
	for _, u := range m.users {
		list = append(list, *u)
	}
	return list, nil
}

func (m *mockUserRepo) UpdatePreferences(ctx context.Context, userID string, showAdult bool, adminMode int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID.Hex() == userID || u.Username == userID {
			u.ShowAdult = showAdult
			u.AdminMode = adminMode
			return nil
		}
	}
	return domain.ErrNotFound
}

type mockCollectionRepo struct {
	mu   sync.Mutex
	user *domain.User
}

func newMockCollectionRepo(user *domain.User) *mockCollectionRepo {
	return &mockCollectionRepo{user: user}
}

func (m *mockCollectionRepo) GetUser(ctx context.Context, userIDOrUsername string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.user != nil {
		return m.user, nil
	}
	return nil, domain.ErrNotFound
}

func (m *mockCollectionRepo) UpdateCollections(ctx context.Context, userID string, collections []domain.UserCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.user != nil {
		m.user.Collections = collections
		return nil
	}
	return domain.ErrNotFound
}

func (m *mockCollectionRepo) IncrementVersion(ctx context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.user != nil {
		m.user.CollectionVersion++
		return m.user.CollectionVersion, nil
	}
	return 0, domain.ErrNotFound
}

func (m *mockCollectionRepo) FollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	return nil
}

func (m *mockCollectionRepo) UnfollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	return nil
}

func (m *mockCollectionRepo) GetFollowers(ctx context.Context, username string) ([]string, error) {
	return []string{"follower1"}, nil
}

func (m *mockCollectionRepo) GetFollowing(ctx context.Context, username string) ([]string, error) {
	return []string{"following1"}, nil
}

func (m *mockCollectionRepo) AddFavoritePerson(ctx context.Context, userID string, person any) error {
	return nil
}

func (m *mockCollectionRepo) RemoveFavoritePerson(ctx context.Context, userID string, personID int) error {
	return nil
}

func (m *mockCollectionRepo) SetFavoritePrivacy(ctx context.Context, userID string, isPublic bool) error {
	return nil
}

func (m *mockCollectionRepo) GetPublishedCollections(ctx context.Context) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
