package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"soulstash/internal/domain"
	"soulstash/internal/repository"
)

const (
	CollectionNameMaxLength    = 25
	PublishMinCollectionTitles = 6
	DefaultCollectionWatched   = "Watched"
	DefaultCollectionWatchlist = "Watchlist"
)

var defaultCollectionNames = map[string]bool{
	DefaultCollectionWatched:   true,
	DefaultCollectionWatchlist: true,
}

// CollectionMutationResult represents standard collection mutation payload
type CollectionMutationResult struct {
	Message           string                  `json:"message"`
	CollectionVersion int                     `json:"collectionVersion"`
	StaleResolved     bool                    `json:"staleResolved"`
	Collections       []domain.UserCollection `json:"collections"`
	Extra             map[string]any          `json:"extra,omitempty"`
}

type CollectionService interface {
	GetCollections(ctx context.Context, userID string) ([]domain.UserCollection, int, error)
	CreateCollection(ctx context.Context, userID, name, description string, isPublic bool, clientVersion *int) (*CollectionMutationResult, error)
	UpdateCollection(ctx context.Context, userID, collectionID string, update UpdateCollectionInput, clientVersion *int) (*CollectionMutationResult, error)
	DeleteCollection(ctx context.Context, userID, collectionID string, clientVersion *int) (*CollectionMutationResult, error)
	AddMovie(ctx context.Context, userID, collectionID string, movie map[string]any, clientVersion *int) (*CollectionMutationResult, error)
	RemoveMovie(ctx context.Context, userID, collectionID string, contentID any, clientVersion *int) (*CollectionMutationResult, error)
	GetCollectionMovies(ctx context.Context, userID, collectionID string) ([]any, error)
	PublishCollection(ctx context.Context, userID, collectionID string, publish bool, clientVersion *int) (*CollectionMutationResult, error)
	ReorderCollections(ctx context.Context, userID string, order []string, clientVersion *int) (*CollectionMutationResult, error)
	GetPublicProfile(ctx context.Context, currentUsername, targetUsername string) (map[string]any, error)
	Follow(ctx context.Context, currentUsername, targetUsername string) error
	Unfollow(ctx context.Context, currentUsername, targetUsername string) error
	GetFollowers(ctx context.Context, username string) ([]string, error)
	GetFollowing(ctx context.Context, username string) ([]string, error)
	GetFavorites(ctx context.Context, userID string) ([]any, error)
	AddFavorite(ctx context.Context, userID string, person any) error
	RemoveFavorite(ctx context.Context, userID string, personID int) error
	SetFavoritesPrivacy(ctx context.Context, userID string, isPublic bool) error
	GetPublicCollection(ctx context.Context, username, collectionName string) (*domain.UserCollection, error)
	GetPublishedCollections(ctx context.Context) ([]map[string]any, error)
	DetectAnime(ctx context.Context, items []map[string]any) []map[string]any
}

type UpdateCollectionInput struct {
	Name        *string
	Description *string
	Banner      *string
	IsPublic    *bool
	Movies      *[]any
}

type collectionService struct {
	repo repository.CollectionRepository
}

// NewCollectionService creates a new CollectionService
func NewCollectionService(repo repository.CollectionRepository) CollectionService {
	return &collectionService{repo: repo}
}

func (s *collectionService) GetCollections(ctx context.Context, userID string) ([]domain.UserCollection, int, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	cols := sanitizeCollections(user.Collections)
	return cols, user.CollectionVersion, nil
}

func (s *collectionService) CreateCollection(ctx context.Context, userID, name, description string, isPublic bool, clientVersion *int) (*CollectionMutationResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Collection name is required", 400)
	}
	if len(name) > CollectionNameMaxLength {
		return nil, domain.NewAppError(domain.ErrBadRequest, fmt.Sprintf("Collection name must be between 1 and %d characters", CollectionNameMaxLength), 400)
	}

	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	for _, col := range user.Collections {
		if strings.EqualFold(col.Name, name) {
			return nil, domain.NewAppError(domain.ErrConflict, "Collection with this name already exists", 409)
		}
	}

	now := time.Now()
	newCol := domain.UserCollection{
		ID:          name,
		Name:        name,
		Description: description,
		IsDeletable: true,
		IsPublic:    isPublic,
		IsPublished: false,
		Banner:      "https://cdn.imgchest.com/files/b23d0bfcaa8b.jpg",
		MovieCount:  0,
		Movies:      []any{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	updatedCollections := append(user.Collections, newCol)
	if err := s.repo.UpdateCollections(ctx, userID, updatedCollections); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           fmt.Sprintf(`Collection "%s" created successfully!`, name),
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(updatedCollections),
		Extra: map[string]any{
			"_id":         name,
			"name":        name,
			"isDeletable": true,
			"isPublic":    isPublic,
		},
	}, nil
}

func (s *collectionService) UpdateCollection(ctx context.Context, userID, collectionID string, update UpdateCollectionInput, clientVersion *int) (*CollectionMutationResult, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	col := user.Collections[idx]
	isDefault := defaultCollectionNames[col.Name]

	if update.Name != nil {
		targetName := strings.TrimSpace(*update.Name)
		if len(targetName) < 1 || len(targetName) > CollectionNameMaxLength {
			return nil, domain.NewAppError(domain.ErrBadRequest, fmt.Sprintf("Collection name must be between 1 and %d characters", CollectionNameMaxLength), 400)
		}
		if isDefault && targetName != col.Name {
			return nil, domain.NewAppError(domain.ErrBadRequest, "Default collection names cannot be changed", 400)
		}
		if !strings.EqualFold(targetName, col.Name) {
			for i, existing := range user.Collections {
				if i != idx && strings.EqualFold(existing.Name, targetName) {
					return nil, domain.NewAppError(domain.ErrConflict, "Collection with this name already exists", 409)
				}
			}
		}
		col.Name = targetName
	}

	if update.IsPublic != nil {
		if col.IsPublished && !*update.IsPublic {
			return nil, domain.NewAppError(domain.ErrBadRequest, "Unpublish this collection before making it private", 400)
		}
		col.IsPublic = *update.IsPublic
	}

	if update.Description != nil {
		col.Description = *update.Description
	}
	if update.Banner != nil {
		col.Banner = *update.Banner
	}
	if update.Movies != nil {
		col.Movies = *update.Movies
		col.MovieCount = len(*update.Movies)
	}

	col.UpdatedAt = time.Now()
	user.Collections[idx] = col

	if err := s.repo.UpdateCollections(ctx, userID, user.Collections); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           fmt.Sprintf(`Collection "%s" updated successfully!`, col.Name),
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(user.Collections),
	}, nil
}

func (s *collectionService) DeleteCollection(ctx context.Context, userID, collectionID string, clientVersion *int) (*CollectionMutationResult, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	col := user.Collections[idx]
	if !col.IsDeletable || defaultCollectionNames[col.Name] {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Default collections cannot be deleted", 400)
	}

	updated := append(user.Collections[:idx], user.Collections[idx+1:]...)
	if err := s.repo.UpdateCollections(ctx, userID, updated); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           fmt.Sprintf(`Collection "%s" deleted successfully!`, col.Name),
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(updated),
	}, nil
}

func (s *collectionService) AddMovie(ctx context.Context, userID, collectionID string, movie map[string]any, clientVersion *int) (*CollectionMutationResult, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	movie["addedAt"] = time.Now()
	user.Collections[idx].Movies = append(user.Collections[idx].Movies, movie)
	user.Collections[idx].MovieCount = len(user.Collections[idx].Movies)
	user.Collections[idx].UpdatedAt = time.Now()

	if err := s.repo.UpdateCollections(ctx, userID, user.Collections); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           "Item added to collection",
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(user.Collections),
	}, nil
}

func (s *collectionService) RemoveMovie(ctx context.Context, userID, collectionID string, contentID any, clientVersion *int) (*CollectionMutationResult, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	targetStr := normalizeID(contentID)
	var filtered []any
	for _, m := range user.Collections[idx].Movies {
		mid := extractMovieID(m)
		if mid != "" && mid == targetStr {
			continue // removed!
		}
		filtered = append(filtered, m)
	}

	user.Collections[idx].Movies = filtered
	user.Collections[idx].MovieCount = len(filtered)
	user.Collections[idx].UpdatedAt = time.Now()

	if err := s.repo.UpdateCollections(ctx, userID, user.Collections); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           "Item removed from collection",
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(user.Collections),
	}, nil
}

func (s *collectionService) GetCollectionMovies(ctx context.Context, userID, collectionID string) ([]any, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	if user.Collections[idx].Movies == nil {
		return []any{}, nil
	}
	return user.Collections[idx].Movies, nil
}

func (s *collectionService) PublishCollection(ctx context.Context, userID, collectionID string, publish bool, clientVersion *int) (*CollectionMutationResult, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	idx := findCollectionIndex(user.Collections, collectionID)
	if idx == -1 {
		return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
	}

	col := user.Collections[idx]
	if defaultCollectionNames[col.Name] {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Default collections cannot be published", 400)
	}

	if publish && len(col.Movies) < PublishMinCollectionTitles {
		return nil, domain.NewAppError(domain.ErrBadRequest, fmt.Sprintf("At least %d titles are required to publish this collection", PublishMinCollectionTitles), 400)
	}

	col.IsPublished = publish
	if publish {
		col.IsPublic = true
	}
	col.UpdatedAt = time.Now()
	user.Collections[idx] = col

	if err := s.repo.UpdateCollections(ctx, userID, user.Collections); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	msg := "Collection unpublished"
	if publish {
		msg = "Collection published"
	}

	return &CollectionMutationResult{
		Message:           msg,
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(user.Collections),
	}, nil
}

func (s *collectionService) ReorderCollections(ctx context.Context, userID string, order []string, clientVersion *int) (*CollectionMutationResult, error) {
	if len(order) == 0 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Collection order is required", 400)
	}

	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]domain.UserCollection)
	for _, c := range user.Collections {
		byKey[fmt.Sprintf("%v", c.ID)] = c
		byKey[c.Name] = c
	}

	var reordered []domain.UserCollection
	seen := make(map[string]bool)

	for _, key := range order {
		if c, ok := byKey[key]; ok && !seen[c.Name] {
			reordered = append(reordered, c)
			seen[c.Name] = true
		}
	}

	for _, c := range user.Collections {
		if !seen[c.Name] {
			reordered = append(reordered, c)
		}
	}

	if err := s.repo.UpdateCollections(ctx, userID, reordered); err != nil {
		return nil, err
	}

	stale := clientVersion != nil && *clientVersion != user.CollectionVersion
	nextVersion := user.CollectionVersion + 1

	return &CollectionMutationResult{
		Message:           "Collections reordered successfully",
		CollectionVersion: nextVersion,
		StaleResolved:     stale,
		Collections:       sanitizeCollections(reordered),
	}, nil
}

func (s *collectionService) GetPublicProfile(ctx context.Context, currentUsername, targetUsername string) (map[string]any, error) {
	targetUser, err := s.repo.GetUser(ctx, targetUsername)
	if err != nil {
		return nil, domain.NewAppError(domain.ErrNotFound, "User not found", 404)
	}

	var publicCollections []domain.UserCollection
	for _, c := range targetUser.Collections {
		if c.IsPublic {
			publicCollections = append(publicCollections, c)
		}
	}

	isFollowing := false
	isFollowedBy := false

	if currentUsername != "" {
		for _, u := range targetUser.Followers {
			if strings.EqualFold(u, currentUsername) {
				isFollowing = true
				break
			}
		}
		for _, u := range targetUser.Following {
			if strings.EqualFold(u, currentUsername) {
				isFollowedBy = true
				break
			}
		}
	}

	return map[string]any{
		"user": map[string]any{
			"username":             targetUser.Username,
			"fullName":             targetUser.FullName,
			"firstName":            targetUser.FirstName,
			"lastName":             targetUser.LastName,
			"dateOfBirth":          targetUser.DateOfBirth,
			"avatar":               targetUser.Avatar,
			"bio":                  targetUser.Bio,
			"instagramHandle":      targetUser.InstagramHandle,
			"xHandle":              targetUser.XHandle,
			"youtubeHandle":        targetUser.YoutubeHandle,
			"followersCount":       len(targetUser.Followers),
			"followingCount":       len(targetUser.Following),
			"collections":          publicCollections,
			"favoritePeople":       targetUser.FavoritePeople,
			"favoritePeoplePublic": targetUser.FavoritePeoplePublic,
		},
		"isOwner":      strings.EqualFold(currentUsername, targetUsername),
		"isFollowing":  isFollowing,
		"isFollowedBy": isFollowedBy,
	}, nil
}

func (s *collectionService) Follow(ctx context.Context, currentUsername, targetUsername string) error {
	if strings.EqualFold(currentUsername, targetUsername) {
		return domain.NewAppError(domain.ErrBadRequest, "Cannot follow yourself", 400)
	}
	return s.repo.FollowUser(ctx, currentUsername, targetUsername)
}

func (s *collectionService) Unfollow(ctx context.Context, currentUsername, targetUsername string) error {
	return s.repo.UnfollowUser(ctx, currentUsername, targetUsername)
}

func (s *collectionService) GetFollowers(ctx context.Context, username string) ([]string, error) {
	return s.repo.GetFollowers(ctx, username)
}

func (s *collectionService) GetFollowing(ctx context.Context, username string) ([]string, error) {
	return s.repo.GetFollowing(ctx, username)
}

func (s *collectionService) GetFavorites(ctx context.Context, userID string) ([]any, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.FavoritePeople == nil {
		return []any{}, nil
	}
	return user.FavoritePeople, nil
}

func (s *collectionService) AddFavorite(ctx context.Context, userID string, person any) error {
	return s.repo.AddFavoritePerson(ctx, userID, person)
}

func (s *collectionService) RemoveFavorite(ctx context.Context, userID string, personID int) error {
	return s.repo.RemoveFavoritePerson(ctx, userID, personID)
}

func (s *collectionService) SetFavoritesPrivacy(ctx context.Context, userID string, isPublic bool) error {
	return s.repo.SetFavoritePrivacy(ctx, userID, isPublic)
}

func (s *collectionService) GetPublicCollection(ctx context.Context, username, collectionName string) (*domain.UserCollection, error) {
	user, err := s.repo.GetUser(ctx, username)
	if err != nil {
		return nil, domain.NewAppError(domain.ErrNotFound, "User not found", 404)
	}

	for _, c := range user.Collections {
		if (strings.EqualFold(c.Name, collectionName) || fmt.Sprintf("%v", c.ID) == collectionName) && c.IsPublic {
			return &c, nil
		}
	}
	return nil, domain.NewAppError(domain.ErrNotFound, "Collection not found", 404)
}

func (s *collectionService) GetPublishedCollections(ctx context.Context) ([]map[string]any, error) {
	return s.repo.GetPublishedCollections(ctx)
}

func (s *collectionService) DetectAnime(ctx context.Context, items []map[string]any) []map[string]any {
	var results []map[string]any
	asianCountries := map[string]bool{"JP": true, "CN": true, "KR": true}
	asianLangs := map[string]bool{"ja": true, "zh": true, "ko": true}

	for _, item := range items {
		isAnime := false
		lang, _ := item["original_language"].(string)

		hasAnimation := false
		if genres, ok := item["genres"].([]any); ok {
			for _, g := range genres {
				if gStr, ok := g.(string); ok && gStr == "Animation" {
					hasAnimation = true
					break
				}
				if gMap, ok := g.(map[string]any); ok && gMap["name"] == "Animation" {
					hasAnimation = true
					break
				}
			}
		}

		if hasAnimation && asianLangs[lang] {
			if origin, ok := item["origin_country"].([]any); ok {
				for _, c := range origin {
					if cStr, ok := c.(string); ok && asianCountries[cStr] {
						isAnime = true
						break
					}
				}
			} else if originStr, ok := item["origin_country"].(string); ok && asianCountries[originStr] {
				isAnime = true
			}
		}

		results = append(results, map[string]any{
			"id":      item["id"],
			"isAnime": isAnime,
		})
	}
	return results
}

func findCollectionIndex(collections []domain.UserCollection, identifier string) int {
	cleanID := strings.TrimSpace(identifier)
	for i, c := range collections {
		if strings.EqualFold(c.Name, cleanID) {
			return i
		}
		if c.ID != nil {
			switch vid := c.ID.(type) {
			case bson.ObjectID:
				if vid.Hex() == cleanID {
					return i
				}
			case string:
				if vid == cleanID {
					return i
				}
			default:
				if fmt.Sprintf("%v", vid) == cleanID {
					return i
				}
			}
		}
	}
	return -1
}

func extractMovieID(m any) string {
	switch v := m.(type) {
	case map[string]any:
		for k, val := range v {
			lk := strings.ToLower(k)
			if lk == "movieid" || lk == "seriesid" || lk == "id" || lk == "mediaid" || lk == "tmdbid" ||
				lk == "movie_id" || lk == "series_id" || lk == "media_id" || lk == "tmdb_id" || lk == "contentid" || lk == "content_id" {
				if val != nil {
					return normalizeID(val)
				}
			}
		}
	case bson.M:
		for k, val := range v {
			lk := strings.ToLower(k)
			if lk == "movieid" || lk == "seriesid" || lk == "id" || lk == "mediaid" || lk == "tmdbid" ||
				lk == "movie_id" || lk == "series_id" || lk == "media_id" || lk == "tmdb_id" || lk == "contentid" || lk == "content_id" {
				if val != nil {
					return normalizeID(val)
				}
			}
		}
	case bson.D:
		for _, elem := range v {
			lk := strings.ToLower(elem.Key)
			if lk == "movieid" || lk == "seriesid" || lk == "id" || lk == "mediaid" || lk == "tmdbid" ||
				lk == "movie_id" || lk == "series_id" || lk == "media_id" || lk == "tmdb_id" || lk == "contentid" || lk == "content_id" {
				if elem.Value != nil {
					return normalizeID(elem.Value)
				}
			}
		}
	case float64, float32, int, int32, int64, string:
		return normalizeID(v)
	}
	return ""
}

func normalizeID(val any) string {
	switch v := val.(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case float32:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return strings.TrimSpace(v)
	case bson.ObjectID:
		return v.Hex()
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func sanitizeCollections(cols []domain.UserCollection) []domain.UserCollection {
	res := make([]domain.UserCollection, len(cols))
	for i, c := range cols {
		if c.ID == nil || c.ID == "" {
			c.ID = c.Name
		}
		if c.Movies == nil {
			c.Movies = []any{}
		}
		c.MovieCount = len(c.Movies)
		res[i] = c
	}
	return res
}
