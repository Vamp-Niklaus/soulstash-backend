package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/mongodb"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// UserRepository defines the interface for user persistence operations
type UserRepository interface {
	FindByID(ctx context.Context, id string) (*domain.User, error)
	FindByUsername(ctx context.Context, username string) (*domain.User, error)
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error)
	Create(ctx context.Context, user *domain.User) error
	Update(ctx context.Context, id string, update bson.M) error
	SavePasswordReset(ctx context.Context, email, otp string, expiresAt time.Time) error
	FindValidPasswordReset(ctx context.Context, email, otp string) (string, error)
	DeletePasswordReset(ctx context.Context, resetID string) error
	UpdatePassword(ctx context.Context, userID, hashedPassword string) error
	FindAllUsers(ctx context.Context) ([]domain.User, error)
	UpdatePreferences(ctx context.Context, userID string, showAdult bool, adminMode int) error
	GetWatchProgress(ctx context.Context, userID string) ([]map[string]any, error)
	SaveWatchProgress(ctx context.Context, userID string, item map[string]any) error
	DeleteWatchProgress(ctx context.Context, userID, key string) error
	ClearWatchProgress(ctx context.Context, userID string) error
	GetWatchedEpisodes(ctx context.Context, userID string) ([]string, error)
	AddWatchedEpisodes(ctx context.Context, userID string, epKeys []string) error
	RemoveWatchedEpisode(ctx context.Context, userID, epKey string) error
}

type mongoUserRepo struct {
	db *mongo.Database
}

// NewUserRepository creates a new MongoDB user repository
func NewUserRepository(db *mongo.Database) UserRepository {
	return &mongoUserRepo{db: db}
}

func parseObjectIDOrUsername(id string) (bson.ObjectID, bool) {
	objID, err := bson.ObjectIDFromHex(id)
	if err == nil && !objID.IsZero() {
		return objID, true
	}
	return bson.ObjectID{}, false
}

func (r *mongoUserRepo) FindByID(ctx context.Context, id string) (*domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)

	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(id); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = id
	}

	var user domain.User
	err := coll.FindOne(ctx, filter).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user find error: %w", err)
	}
	return &user, nil
}

func (r *mongoUserRepo) FindByUsername(ctx context.Context, username string) (*domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{
		"username": bson.M{
			"$regex":   fmt.Sprintf("^%s$", regexp.QuoteMeta(strings.TrimSpace(username))),
			"$options": "i",
		},
	}

	var user domain.User
	err := coll.FindOne(ctx, filter).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user find by username error: %w", err)
	}
	return &user, nil
}

func (r *mongoUserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	var user domain.User
	err := r.db.Collection(mongodb.CollectionUsers).FindOne(
		ctx,
		bson.M{"email": strings.TrimSpace(email)},
		options.FindOne().SetCollation(&options.Collation{Locale: "en", Strength: 2}),
	).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user find by email error: %w", err)
	}
	return &user, nil
}

func (r *mongoUserRepo) FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{
		"$or": []bson.M{
			{"email": identifier},
			{"username": identifier},
		},
	}

	var user domain.User
	err := coll.FindOne(ctx, filter, options.FindOne().SetCollation(&options.Collation{Locale: "en", Strength: 2})).Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user find by email/username error: %w", err)
	}
	return &user, nil
}

func (r *mongoUserRepo) Create(ctx context.Context, user *domain.User) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	if user.ID.IsZero() {
		user.ID = bson.NewObjectID()
	}
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	_, err := coll.InsertOne(ctx, user)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("user insert error: %w", err)
	}
	return nil
}

func (r *mongoUserRepo) Update(ctx context.Context, id string, update bson.M) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(id); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = id
	}

	updateDoc := update
	hasOperator := false
	for k := range update {
		if strings.HasPrefix(k, "$") {
			hasOperator = true
			break
		}
	}
	if !hasOperator {
		updateDoc = bson.M{"$set": update}
	}

	res, err := coll.UpdateOne(ctx, filter, updateDoc)
	if err != nil {
		return fmt.Errorf("user update error: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *mongoUserRepo) SavePasswordReset(ctx context.Context, email, otp string, expiresAt time.Time) error {
	coll := r.db.Collection(mongodb.CollectionPasswordReset)
	doc := bson.M{
		"_id":       bson.NewObjectID(),
		"email":     email,
		"otp":       otp,
		"createdAt": time.Now(),
		"expiresAt": expiresAt,
	}
	_, err := coll.InsertOne(ctx, doc)
	return err
}

func (r *mongoUserRepo) FindValidPasswordReset(ctx context.Context, email, otp string) (string, error) {
	coll := r.db.Collection(mongodb.CollectionPasswordReset)
	filter := bson.M{
		"email":     email,
		"otp":       otp,
		"expiresAt": bson.M{"$gt": time.Now()},
	}

	var doc struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := coll.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	return doc.ID.Hex(), nil
}

func (r *mongoUserRepo) DeletePasswordReset(ctx context.Context, resetID string) error {
	coll := r.db.Collection(mongodb.CollectionPasswordReset)
	objID, err := bson.ObjectIDFromHex(resetID)
	if err != nil {
		return err
	}
	_, err = coll.DeleteOne(ctx, bson.M{"_id": objID})
	return err
}

func (r *mongoUserRepo) UpdatePassword(ctx context.Context, userID, hashedPassword string) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"password":  hashedPassword,
			"updatedAt": time.Now(),
		},
	})
	return err
}

func (r *mongoUserRepo) FindAllUsers(ctx context.Context) ([]domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	findOpts := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetProjection(bson.M{"password": 0})

	cursor, err := coll.Find(ctx, bson.M{}, findOpts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var users []domain.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (r *mongoUserRepo) UpdatePreferences(ctx context.Context, userID string, showAdult bool, adminMode int) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"showAdult": showAdult,
			"adminMode": adminMode,
			"updatedAt": time.Now(),
		},
	})
	return err
}

var seriesEpisodeSuffixRe = regexp.MustCompile(`_s[^_]*_e[^_]*$`)

func userFilter(userID string) bson.M {
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}
	return filter
}

func watchProgressPullFilter(key string, item map[string]any) bson.M {
	seriesGroup := ""
	if item != nil {
		seriesGroup, _ = item["seriesGroup"].(string)
	}
	if seriesGroup == "" {
		stripped := seriesEpisodeSuffixRe.ReplaceAllString(key, "")
		if stripped != "" && stripped != key {
			seriesGroup = stripped
		}
	}
	if seriesGroup != "" {
		return bson.M{
			"$or": []bson.M{
				{"key": key},
				{"seriesGroup": seriesGroup},
				{"key": bson.M{"$regex": "^" + regexp.QuoteMeta(seriesGroup) + `_s[^_]*_e[^_]*$`}},
			},
		}
	}
	return bson.M{"key": key}
}

func (r *mongoUserRepo) GetWatchProgress(ctx context.Context, userID string) ([]map[string]any, error) {
	user, err := r.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.WatchProgress == nil {
		return []map[string]any{}, nil
	}
	return user.WatchProgress, nil
}

func (r *mongoUserRepo) SaveWatchProgress(ctx context.Context, userID string, item map[string]any) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := userFilter(userID)

	key, _ := item["key"].(string)
	if key == "" {
		return nil
	}

	// Remove previous progress entries for this title or series so only the newest episode remains.
	_, _ = coll.UpdateOne(ctx, filter, bson.M{
		"$pull": bson.M{"watchProgress": watchProgressPullFilter(key, item)},
	})

	// Store updatedAt in Unix milliseconds to match browser Date.now()
	item["updatedAt"] = time.Now().UnixMilli()
	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$push": bson.M{
			"watchProgress": bson.M{
				"$each":     []map[string]any{item},
				"$position": 0,
				"$slice":    30,
			},
		},
		"$set": bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoUserRepo) DeleteWatchProgress(ctx context.Context, userID, key string) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := userFilter(userID)

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$pull": bson.M{"watchProgress": watchProgressPullFilter(key, nil)},
		"$set":  bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoUserRepo) ClearWatchProgress(ctx context.Context, userID string) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := userFilter(userID)

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"watchProgress": []map[string]any{},
			"updatedAt":     time.Now(),
		},
	})
	return err
}

func (r *mongoUserRepo) GetWatchedEpisodes(ctx context.Context, userID string) ([]string, error) {
	user, err := r.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.WatchedEpisodes == nil {
		return []string{}, nil
	}
	return user.WatchedEpisodes, nil
}

func (r *mongoUserRepo) AddWatchedEpisodes(ctx context.Context, userID string, epKeys []string) error {
	clean := make([]string, 0, len(epKeys))
	seen := make(map[string]bool, len(epKeys))
	for _, k := range epKeys {
		trimmed := strings.ToLower(strings.TrimSpace(k))
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		clean = append(clean, trimmed)
	}
	if len(clean) == 0 {
		return nil
	}

	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := userFilter(userID)

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$addToSet": bson.M{
			"watchedEpisodes": bson.M{"$each": clean},
		},
		"$set": bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoUserRepo) RemoveWatchedEpisode(ctx context.Context, userID, epKey string) error {
	trimmed := strings.ToLower(strings.TrimSpace(epKey))
	if trimmed == "" {
		return nil
	}
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := userFilter(userID)

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$pull": bson.M{"watchedEpisodes": trimmed},
		"$set":  bson.M{"updatedAt": time.Now()},
	})
	return err
}
