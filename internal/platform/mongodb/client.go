package mongodb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"soulstash/internal/config"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	CollectionUsers         = "users"
	CollectionRatings       = "Ratings"
	CollectionPlayerSources = "PlayerSources"
	CollectionSourceConfigs = "SourceConfigs"
	CollectionMetrics       = "AppMetrics"
	CollectionTrafficLogs   = "TrafficLogsMonolith"
	CollectionAdminSettings = "admin_setting_monolith"
	CollectionPasswordReset = "password_resets"

	DocIDRatingsMetrics = "ratings_api_counters"
	DocIDMultimovies    = "multimovies"
)

// Client wraps the official mongo.Client and selected database
type Client struct {
	client *mongo.Client
	db     *mongo.Database
	logger *slog.Logger
}

// Connect initializes and verifies connection to MongoDB
func Connect(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Client, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(cfg.MongoDBURI)
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create mongo client: %w", err)
	}

	if err := client.Ping(connectCtx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping mongo server: %w", err)
	}

	db := client.Database(cfg.MongoDBName)
	logger.Info("Connected to MongoDB", slog.String("database", db.Name()))

	// Verify database connection as in Node (db.collection('details').findOne())
	var dummy bson.M
	_ = db.Collection("details").FindOne(connectCtx, bson.M{}).Decode(&dummy)

	c := &Client{
		client: client,
		db:     db,
		logger: logger,
	}

	return c, nil
}

// InitIndexes initializes required unique and lookup indexes
func (c *Client) InitIndexes(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 1. users: unique username index
	usersCol := c.db.Collection(CollectionUsers)
	_, err := usersCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "username", Value: 1}},
		Options: options.Index().
			SetUnique(true).
			SetName("username_unique_index"),
	})
	if err != nil {
		c.logger.Warn("Failed to create username unique index", slog.String("error", err.Error()))
	}
	// Enforce the same case-insensitive uniqueness used by signup checks.
	caseInsensitive := &options.Collation{Locale: "en", Strength: 2}
	_, err = usersCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys:    bson.D{{Key: "username", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("username_case_insensitive_unique_index").SetCollation(caseInsensitive),
	})
	if err != nil {
		c.logger.Warn("Failed to create case-insensitive username unique index", slog.String("error", err.Error()))
	}
	_, err = usersCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("email_case_insensitive_unique_index").
			SetCollation(caseInsensitive).
			SetPartialFilterExpression(bson.M{"email": bson.M{"$type": "string", "$gt": ""}}),
	})
	if err != nil {
		c.logger.Warn("Failed to create case-insensitive email unique index", slog.String("error", err.Error()))
	}

	// 2. Ratings: unique tmdbID + mediaType
	ratingsCol := c.db.Collection(CollectionRatings)
	_, err = ratingsCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "tmdbID", Value: 1},
			{Key: "mediaType", Value: 1},
		},
		Options: options.Index().
			SetUnique(true).
			SetName("ratings_tmdb_media_unique"),
	})
	if err != nil {
		c.logger.Warn("Failed to create ratings tmdb unique index", slog.String("error", err.Error()))
	}

	// 3. Ratings: sparse imdbID + mediaType
	_, err = ratingsCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "imdbID", Value: 1},
			{Key: "mediaType", Value: 1},
		},
		Options: options.Index().
			SetSparse(true).
			SetName("ratings_imdb_media_index"),
	})
	if err != nil {
		c.logger.Warn("Failed to create ratings imdb sparse index", slog.String("error", err.Error()))
	}

	// 4. PlayerSources: drop obsolete player_sources_search_key_unique if exists
	playerSourcesCol := c.db.Collection(CollectionPlayerSources)
	_ = playerSourcesCol.Indexes().DropOne(initCtx, "player_sources_search_key_unique")

	// 5. PlayerSources: lookup index
	_, err = playerSourcesCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "tmdbId", Value: 1},
			{Key: "mediaType", Value: 1},
			{Key: "seasonNumber", Value: 1},
			{Key: "episodeNumber", Value: 1},
		},
		Options: options.Index().
			SetName("player_sources_tmdb_lookup"),
	})
	if err != nil {
		c.logger.Warn("Failed to create player sources lookup index", slog.String("error", err.Error()))
	}

	// 6. PlayerSources: sparse imdbId lookup
	_, err = playerSourcesCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "imdbId", Value: 1},
			{Key: "mediaType", Value: 1},
		},
		Options: options.Index().
			SetSparse(true).
			SetName("player_sources_imdb_lookup"),
	})
	if err != nil {
		c.logger.Warn("Failed to create player sources imdb lookup index", slog.String("error", err.Error()))
	}

	// 7. Seed initial metrics doc if not exists
	metricsCol := c.db.Collection(CollectionMetrics)
	_, _ = metricsCol.UpdateOne(initCtx,
		bson.M{"_id": DocIDRatingsMetrics},
		bson.M{
			"$setOnInsert": bson.M{
				"tmdbDetailFetchCount": 0,
				"omdbFetchCount":       0,
				"createdAt":            time.Now(),
			},
			"$set": bson.M{"updatedAt": time.Now()},
		},
		options.UpdateOne().SetUpsert(true),
	)

	// 8. Seed initial SourceConfigs for multimovies
	sourceConfigsCol := c.db.Collection(CollectionSourceConfigs)
	_, _ = sourceConfigsCol.UpdateOne(initCtx,
		bson.M{"_id": DocIDMultimovies},
		bson.M{
			"$setOnInsert": bson.M{
				"className": "multimovies",
				"available": true,
				"rootUrls":  []string{"https://multimovies.wtf/"},
				"baseUrls":  []string{"https://multimovies.fyi/"},
				"createdAt": time.Now(),
			},
			"$set": bson.M{"updatedAt": time.Now()},
		},
		options.UpdateOne().SetUpsert(true),
	)

	// 9. TrafficLogsMonolith: TTL index for 30 days
	trafficCol := c.db.Collection(CollectionTrafficLogs)
	expireSeconds := int32(30 * 24 * 60 * 60)
	_, _ = trafficCol.Indexes().CreateOne(initCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "createdAt", Value: 1}},
		Options: options.Index().
			SetExpireAfterSeconds(expireSeconds).
			SetName("traffic_ttl_30d"),
	})

	c.logger.Info("Database initialized with indexes and configuration records")
	return nil
}

// Database returns the underlying mongo.Database handle
func (c *Client) Database() *mongo.Database {
	return c.db
}

// Disconnect gracefully closes the connection pool
func (c *Client) Disconnect(ctx context.Context) error {
	return c.client.Disconnect(ctx)
}
