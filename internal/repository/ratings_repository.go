package repository

import (
	"context"
	"errors"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/mongodb"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type RatingsRepository interface {
	FindRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error)
	FindRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error)
	UpsertRating(ctx context.Context, record *domain.RatingRecord) error
	IncrementMetric(ctx context.Context, metricField string) error
	GetMetrics(ctx context.Context) (*domain.AppMetrics, error)
}

type mongoRatingsRepo struct {
	db *mongo.Database
}

func NewRatingsRepository(db *mongo.Database) RatingsRepository {
	return &mongoRatingsRepo{db: db}
}

func (r *mongoRatingsRepo) FindRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error) {
	coll := r.db.Collection(mongodb.CollectionRatings)
	filter := bson.M{
		"tmdbID":    tmdbID,
		"mediaType": mediaType,
	}

	var record domain.RatingRecord
	err := coll.FindOne(ctx, filter).Decode(&record)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &record, nil
}

func (r *mongoRatingsRepo) FindRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
	filter := bson.M{}
	if mediaType != "" {
		filter["mediaType"] = mediaType
	}
	if limit < 1 {
		limit = 5000
	}

	cursor, err := r.db.Collection(mongodb.CollectionRatings).Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "updatedAt", Value: -1}, {Key: "tmdbID", Value: 1}}).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	items := make([]domain.RatingRecord, 0)
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *mongoRatingsRepo) UpsertRating(ctx context.Context, record *domain.RatingRecord) error {
	coll := r.db.Collection(mongodb.CollectionRatings)
	filter := bson.M{
		"tmdbID":    record.TmdbID,
		"mediaType": record.MediaType,
	}

	update := bson.M{
		"$set": bson.M{
			"tmdbID":       record.TmdbID,
			"mediaType":    record.MediaType,
			"type":         record.MediaType,
			"imdbID":       record.ImdbID,
			"imdb_rating":  record.ImdbRating,
			"vote_average": record.VoteAverage,
			"updatedAt":    time.Now(),
		},
		"$setOnInsert": bson.M{
			"createdAt": time.Now(),
		},
	}

	_, err := coll.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	return err
}

func (r *mongoRatingsRepo) IncrementMetric(ctx context.Context, metricField string) error {
	coll := r.db.Collection(mongodb.CollectionMetrics)
	_, err := coll.UpdateOne(ctx,
		bson.M{"_id": mongodb.DocIDRatingsMetrics},
		bson.M{
			"$inc": bson.M{metricField: 1},
			"$set": bson.M{"updatedAt": time.Now()},
		},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (r *mongoRatingsRepo) GetMetrics(ctx context.Context) (*domain.AppMetrics, error) {
	coll := r.db.Collection(mongodb.CollectionMetrics)
	var metrics domain.AppMetrics
	err := coll.FindOne(ctx, bson.M{"_id": mongodb.DocIDRatingsMetrics}).Decode(&metrics)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &domain.AppMetrics{
				ID:                   mongodb.DocIDRatingsMetrics,
				TmdbDetailFetchCount: 0,
				OmdbFetchCount:       0,
				UpdatedAt:            time.Now(),
			}, nil
		}
		return nil, err
	}
	return &metrics, nil
}
