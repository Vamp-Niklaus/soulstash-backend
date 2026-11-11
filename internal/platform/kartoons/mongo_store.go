package kartoons

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStore keeps one document, _id "tokens", in the kartoons_auth collection:
//
//	access_token, refresh_token       the current pair (edit these to paste new tokens)
//	access_expires_at, refresh_expires_at, last_refreshed_at, updated_at   (written by the backend)
//	force_refresh                     set to true to make the backend refresh on the next health ping
type MongoStore struct {
	col *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{col: db.Collection("kartoons_auth")}
}

func (s *MongoStore) Load(ctx context.Context) (Record, error) {
	var doc struct {
		Access       string `bson:"access_token"`
		Refresh      string `bson:"refresh_token"`
		ForceRefresh bool   `bson:"force_refresh"`
	}
	err := s.col.FindOne(ctx, bson.M{"_id": "tokens"}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, err
	}
	return Record{Access: doc.Access, Refresh: doc.Refresh, ForceRefresh: doc.ForceRefresh}, nil
}

func (s *MongoStore) Save(ctx context.Context, snap Snapshot) error {
	set := bson.M{
		"access_token":  snap.AccessToken,
		"refresh_token": snap.RefreshToken,
		"updated_at":    time.Now().UTC(),
		"force_refresh": false,
	}
	if snap.AccessExpiresAt != nil {
		set["access_expires_at"] = *snap.AccessExpiresAt
	}
	if snap.RefreshExpiresAt != nil {
		set["refresh_expires_at"] = *snap.RefreshExpiresAt
	}
	if snap.LastRefreshedAt != nil {
		set["last_refreshed_at"] = *snap.LastRefreshedAt
	}
	_, err := s.col.UpdateOne(ctx, bson.M{"_id": "tokens"}, bson.M{"$set": set},
		options.UpdateOne().SetUpsert(true))
	return err
}

func (s *MongoStore) ClearForce(ctx context.Context) error {
	_, err := s.col.UpdateOne(ctx, bson.M{"_id": "tokens"}, bson.M{"$set": bson.M{"force_refresh": false}})
	return err
}

// Ensure creates the document with empty fields and a note, only if it is missing.
func (s *MongoStore) Ensure(ctx context.Context) error {
	_, err := s.col.UpdateOne(ctx,
		bson.M{"_id": "tokens"},
		bson.M{"$setOnInsert": bson.M{
			"access_token":  "",
			"refresh_token": "",
			"force_refresh": false,
			"note":          "Paste access_token and refresh_token here, or use the admin page. The backend reads them on startup and refreshes them.",
			"created_at":    time.Now().UTC(),
		}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}
