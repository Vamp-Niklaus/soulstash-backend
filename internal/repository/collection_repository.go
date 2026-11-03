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
)

// CollectionRepository defines methods for managing user collections and social connections
type CollectionRepository interface {
	GetUser(ctx context.Context, userIDOrUsername string) (*domain.User, error)
	UpdateCollections(ctx context.Context, userID string, collections []domain.UserCollection) error
	IncrementVersion(ctx context.Context, userID string) (int, error)
	FollowUser(ctx context.Context, currentUsername, targetUsername string) error
	UnfollowUser(ctx context.Context, currentUsername, targetUsername string) error
	GetFollowers(ctx context.Context, username string) ([]string, error)
	GetFollowing(ctx context.Context, username string) ([]string, error)
	AddFavoritePerson(ctx context.Context, userID string, person any) error
	RemoveFavoritePerson(ctx context.Context, userID string, personID int) error
	SetFavoritePrivacy(ctx context.Context, userID string, isPublic bool) error
	GetPublishedCollections(ctx context.Context) ([]map[string]any, error)
}

type mongoCollectionRepo struct {
	db *mongo.Database
}

// NewCollectionRepository creates a new collection repository
func NewCollectionRepository(db *mongo.Database) CollectionRepository {
	return &mongoCollectionRepo{db: db}
}

func (r *mongoCollectionRepo) GetUser(ctx context.Context, userIDOrUsername string) (*domain.User, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)

	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userIDOrUsername); ok {
		filter["$or"] = []bson.M{
			{"_id": objID},
			{"username": userIDOrUsername},
		}
	} else {
		filter["username"] = userIDOrUsername
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

func (r *mongoCollectionRepo) UpdateCollections(ctx context.Context, userID string, collections []domain.UserCollection) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"collections": collections,
			"updatedAt":   time.Now(),
		},
		"$inc": bson.M{
			"collectionVersion": 1,
		},
	})
	return err
}

func (r *mongoCollectionRepo) IncrementVersion(ctx context.Context, userID string) (int, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	var updated struct {
		CollectionVersion int `bson:"collectionVersion"`
	}
	err := coll.FindOneAndUpdate(ctx, filter, bson.M{
		"$inc": bson.M{"collectionVersion": 1},
		"$set": bson.M{"updatedAt": time.Now()},
	}).Decode(&updated)
	if err != nil {
		return 0, err
	}
	return updated.CollectionVersion + 1, nil
}

func (r *mongoCollectionRepo) FollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	coll := r.db.Collection(mongodb.CollectionUsers)

	// Add target to current user's following list
	_, err := coll.UpdateOne(ctx, bson.M{"username": currentUsername}, bson.M{
		"$addToSet": bson.M{"following": targetUsername},
		"$set":      bson.M{"updatedAt": time.Now()},
	})
	if err != nil {
		return err
	}

	// Add current user to target's followers list
	_, err = coll.UpdateOne(ctx, bson.M{"username": targetUsername}, bson.M{
		"$addToSet": bson.M{"followers": currentUsername},
		"$set":      bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoCollectionRepo) UnfollowUser(ctx context.Context, currentUsername, targetUsername string) error {
	coll := r.db.Collection(mongodb.CollectionUsers)

	_, err := coll.UpdateOne(ctx, bson.M{"username": currentUsername}, bson.M{
		"$pull": bson.M{"following": targetUsername},
		"$set":  bson.M{"updatedAt": time.Now()},
	})
	if err != nil {
		return err
	}

	_, err = coll.UpdateOne(ctx, bson.M{"username": targetUsername}, bson.M{
		"$pull": bson.M{"followers": currentUsername},
		"$set":  bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoCollectionRepo) GetFollowers(ctx context.Context, username string) ([]string, error) {
	user, err := r.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if user.Followers == nil {
		return []string{}, nil
	}
	return user.Followers, nil
}

func (r *mongoCollectionRepo) GetFollowing(ctx context.Context, username string) ([]string, error) {
	user, err := r.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if user.Following == nil {
		return []string{}, nil
	}
	return user.Following, nil
}

func (r *mongoCollectionRepo) AddFavoritePerson(ctx context.Context, userID string, person any) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$addToSet": bson.M{"favoritePeople": person},
		"$set":      bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoCollectionRepo) RemoveFavoritePerson(ctx context.Context, userID string, personID int) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$pull": bson.M{
			"favoritePeople": bson.M{
				"id": bson.M{
					"$in": []any{personID, float64(personID), int32(personID), int64(personID), fmt.Sprintf("%d", personID)},
				},
			},
		},
		"$set": bson.M{"updatedAt": time.Now()},
	})
	return err
}

func (r *mongoCollectionRepo) SetFavoritePrivacy(ctx context.Context, userID string, isPublic bool) error {
	coll := r.db.Collection(mongodb.CollectionUsers)
	filter := bson.M{}
	if objID, ok := parseObjectIDOrUsername(userID); ok {
		filter["_id"] = objID
	} else {
		filter["username"] = userID
	}

	_, err := coll.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"favoritePeoplePublic": isPublic,
			"updatedAt":            time.Now(),
		},
	})
	return err
}

func (r *mongoCollectionRepo) GetPublishedCollections(ctx context.Context) ([]map[string]any, error) {
	coll := r.db.Collection(mongodb.CollectionUsers)
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$collections"}},
		bson.D{{Key: "$match", Value: bson.M{"collections.isPublished": true}}},
		bson.D{{Key: "$project", Value: bson.M{
			"username":   1,
			"avatar":     1,
			"collection": "$collections",
		}}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []map[string]any
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}
