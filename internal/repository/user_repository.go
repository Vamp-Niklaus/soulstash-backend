package repository

import (
	"context"
	"errors"
	"fmt"
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
	FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error)
	Create(ctx context.Context, user *domain.User) error
	Update(ctx context.Context, id string, update bson.M) error
	SavePasswordReset(ctx context.Context, email, otp string, expiresAt time.Time) error
	FindValidPasswordReset(ctx context.Context, email, otp string) (string, error)
	DeletePasswordReset(ctx context.Context, resetID string) error
	UpdatePassword(ctx context.Context, userID, hashedPassword string) error
	FindAllUsers(ctx context.Context) ([]domain.User, error)
	UpdatePreferences(ctx context.Context, userID string, showAdult bool, adminMode int) error
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
			"$regex":   fmt.Sprintf("^%s$", username),
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

func (r *mongoUserRepo) FindByEmailOrUsername(ctx context.Context, identifier string) (*domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{
		"$or": []bson.M{
			{"email": identifier},
			{"username": identifier},
		},
	}

	var user domain.User
	err := coll.FindOne(ctx, filter).Decode(&user)
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

	res, err := coll.UpdateOne(ctx, filter, update)
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
