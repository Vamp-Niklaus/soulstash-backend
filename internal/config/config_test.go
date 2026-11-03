package config_test

import (
	"os"
	"testing"

	"soulstash/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigLoadAndDefaults(t *testing.T) {
	// Set minimal required env
	t.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	t.Setenv("MONGODB_DB_NAME", "soulstash_test")
	t.Setenv("PORT", "8080")
	t.Setenv("NODE_ENV", "development")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, "mongodb://localhost:27017", cfg.MongoDBURI)
	assert.Equal(t, "soulstash_test", cfg.MongoDBName)
	assert.Equal(t, "development", cfg.AppEnv)
	assert.False(t, cfg.IsProduction)
	assert.Equal(t, "https://api.tmdb.org", cfg.TMDBBaseURL)
	assert.NoError(t, cfg.Validate())
}

func TestConfigValidationFailFast(t *testing.T) {
	os.Unsetenv("MONGODB_URI")
	os.Unsetenv("MONGODB_DB_NAME")

	cfg, err := config.Load()
	require.NoError(t, err)

	err = cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "MONGODB_URI")
}

func TestConfigTMDBHostnameNormalization(t *testing.T) {
	t.Setenv("TMDB_BASE_URL", "https://api.themoviedb.org")
	t.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	t.Setenv("MONGODB_DB_NAME", "test")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "https://api.tmdb.org", cfg.TMDBBaseURL)
}

func TestConfigProductionJWTSecretCheck(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("MONGODB_URI", "mongodb://localhost:27017")
	t.Setenv("MONGODB_DB_NAME", "test")
	t.Setenv("JWT_SECRET", "your-super-secret-jwt-key-change-this-in-production")

	cfg, err := config.Load()
	require.NoError(t, err)
	err = cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET must be configured with a secure key")
}
