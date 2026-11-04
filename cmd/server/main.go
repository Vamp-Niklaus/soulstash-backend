package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"soulstash/internal/config"
	"soulstash/internal/handler"
	"soulstash/internal/platform/email"
	"soulstash/internal/platform/mongodb"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/platform/traffic"
	"soulstash/internal/repository"
	"soulstash/internal/server"
	"soulstash/internal/service"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func main() {
	var logHandler slog.Handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	if os.Getenv("APP_ENV") == "production" || os.Getenv("NODE_ENV") == "production" {
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("Starting Soulstash Go backend server...")

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		logger.Warn("Configuration validation warning (running in partial mode if db missing)", slog.String("error", err.Error()))
	}
	if cfg.BrevoAPIKey == "" {
		logger.Warn("BREVO_API_KEY is not configured; verification and password reset emails will fail")
	}

	var mongoClient *mongodb.Client
	var trafficBuf *traffic.Buffer

	if cfg.MongoDBURI != "" && cfg.MongoDBName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client, err := mongodb.Connect(ctx, cfg, logger)
		cancel()

		if err != nil {
			logger.Error("Failed to connect to database", slog.String("error", err.Error()))
			os.Exit(1)
		}
		mongoClient = client

		if err := mongoClient.InitIndexes(context.Background()); err != nil {
			logger.Warn("Index initialization warning", slog.String("error", err.Error()))
		}

		trafficBuf = traffic.NewBuffer(mongoClient.Database(), logger)
	} else {
		logger.Warn("Running without MongoDB connection (test/mock mode)")
	}

	// Wire Repositories
	var userRepo repository.UserRepository
	var collectionRepo repository.CollectionRepository
	var ratingsRepo repository.RatingsRepository

	if mongoClient != nil {
		userRepo = repository.NewUserRepository(mongoClient.Database())
		collectionRepo = repository.NewCollectionRepository(mongoClient.Database())
		ratingsRepo = repository.NewRatingsRepository(mongoClient.Database())
	}

	// External Clients
	tmdbClient := tmdb.NewClient(cfg.TMDBBaseURL, cfg.TMDBBearerToken)
	brevoClient := email.NewBrevoClient(cfg.BrevoAPIKey)

	// Engines & Services
	adultEngine := service.NewAdultDecisionEngine()
	if mongoClient != nil {
		loadPersistedAdultSettings(context.Background(), mongoClient.Database(), adultEngine, logger)
	}
	authSvc := service.NewAuthService(userRepo, brevoClient, cfg.JWTSecret)
	collectionSvc := service.NewCollectionService(collectionRepo)
	ratingsSvc := service.NewRatingsService(ratingsRepo, tmdbClient, cfg.OMDBAPIKey)

	// Handlers
	authHandler := handler.NewAuthHandler(authSvc, cfg.JWTSecret)
	collectionHandler := handler.NewCollectionHandler(collectionSvc, cfg.JWTSecret)
	contentHandler := handler.NewContentHandler(tmdbClient, ratingsSvc, adultEngine, userRepo, cfg.JWTSecret)
	pagesHandler := handler.NewPagesHandler(cfg.IsProduction)

	var adminHandler *handler.AdminHandler
	if mongoClient != nil {
		adminHandler = handler.NewAdminHandler(userRepo, adultEngine, trafficBuf, tmdbClient, mongoClient.Database(), cfg.JWTSecret)
	}

	deps := &server.HandlerDeps{
		AuthHandler:       authHandler,
		CollectionHandler: collectionHandler,
		ContentHandler:    contentHandler,
		AdminHandler:      adminHandler,
		PagesHandler:      pagesHandler,
	}

	srv := server.New(cfg, logger, trafficBuf, deps)

	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("Server start failure", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	sig := <-shutdownCh

	logger.Info("Received shutdown signal", slog.String("signal", sig.String()))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server shutdown failed", slog.String("error", err.Error()))
	}

	if mongoClient != nil {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer disconnectCancel()
		_ = mongoClient.Disconnect(disconnectCtx)
	}

	logger.Info("Server stopped cleanly")
}

func loadPersistedAdultSettings(ctx context.Context, db *mongo.Database, engine *service.AdultDecisionEngine, logger *slog.Logger) {
	if db == nil || engine == nil {
		return
	}
	var treeDoc struct {
		Tree *service.TreeNode `bson:"tree"`
	}
	if err := db.Collection(mongodb.CollectionAdminSettings).FindOne(ctx, bson.M{"_id": "adult_criteria"}).Decode(&treeDoc); err == nil {
		if treeDoc.Tree != nil {
			engine.SetTree(treeDoc.Tree)
		}
	} else if err != mongo.ErrNoDocuments {
		logger.Warn("Could not load saved adult criteria", slog.String("error", err.Error()))
	}
	var keywordDoc struct {
		Keywords []service.KeywordItem `bson:"keywords"`
	}
	if err := db.Collection(mongodb.CollectionAdminSettings).FindOne(ctx, bson.M{"_id": "adult_keywords_config"}).Decode(&keywordDoc); err == nil {
		if len(keywordDoc.Keywords) > 0 {
			engine.SetKeywords(keywordDoc.Keywords)
		}
	} else if err != mongo.ErrNoDocuments {
		logger.Warn("Could not load saved adult keywords", slog.String("error", err.Error()))
	}
}
