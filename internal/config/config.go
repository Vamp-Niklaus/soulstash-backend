package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all validated configuration for the application
type Config struct {
	Port                  int
	AppEnv                string
	MongoDBURI            string
	MongoDBName           string
	JWTSecret             string
	BrevoAPIKey           string
	TMDBBearerToken       string
	TMDBBaseURL           string
	OMDBAPIKey            string
	WatchmodeAPIKey       string
	CFClearance           string
	PlaywrightBrowserPath string
	ViteDevServerOrigin   string
	IsProduction          bool
}

// Load reads configuration from environment variables and optionally loads .env
func Load() (*Config, error) {
	// Try loading .env in local dev; ignore error if not found
	_ = godotenv.Load()

	appEnv := getEnv("APP_ENV", getEnv("NODE_ENV", "development"))
	isProd := strings.EqualFold(appEnv, "production")

	portStr := getEnv("PORT", "3000")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		port = 3000
	}

	tmdbBaseURL := getEnv("TMDB_BASE_URL", "https://api.tmdb.org")
	// Normalize hostname as in Node: replace api.themoviedb.org with api.tmdb.org
	tmdbBaseURL = strings.ReplaceAll(tmdbBaseURL, "api.themoviedb.org", "api.tmdb.org")

	cfg := &Config{
		Port:                  port,
		AppEnv:                appEnv,
		MongoDBURI:            getEnv("MONGODB_URI", getEnv("MONGO_URI", "")),
		MongoDBName:           getEnv("MONGODB_DB_NAME", getEnv("DB_NAME", "")),
		JWTSecret:             getEnv("JWT_SECRET", "your-super-secret-jwt-key-change-this-in-production"),
		BrevoAPIKey:           getEnv("BREVO_API_KEY", ""),
		TMDBBearerToken:       getEnv("TMDB_BEARER_TOKEN", ""),
		TMDBBaseURL:           tmdbBaseURL,
		OMDBAPIKey:            getEnv("OMDB_API_KEY", ""),
		WatchmodeAPIKey:       getEnv("WATCHMODE_API_KEY", getEnv("WATCHMODE", "")),
		CFClearance:           getEnv("CF_CLEARANCE", ""),
		PlaywrightBrowserPath: getEnv("PLAYWRIGHT_BROWSERS_PATH", "default"),
		ViteDevServerOrigin:   getEnv("VITE_DEV_SERVER_ORIGIN", "http://localhost:5173"),
		IsProduction:          isProd,
	}

	return cfg, nil
}

// Validate checks for required variables and fails fast
func (c *Config) Validate() error {
	var missing []string

	if c.MongoDBURI == "" {
		missing = append(missing, "MONGODB_URI")
	}
	if c.MongoDBName == "" {
		missing = append(missing, "MONGODB_DB_NAME")
	}

	if c.IsProduction && (c.JWTSecret == "" || c.JWTSecret == "your-super-secret-jwt-key-change-this-in-production") {
		return errors.New("JWT_SECRET must be configured with a secure key in production")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	return nil
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}
