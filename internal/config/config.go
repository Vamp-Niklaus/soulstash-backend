package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
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
	OMDBAPIKeys           []string
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

	omdbKeys := getOMDBAPIKeys()
	firstOMDBKey := ""
	if len(omdbKeys) > 0 {
		firstOMDBKey = omdbKeys[0]
	}

	cfg := &Config{
		Port:                  port,
		AppEnv:                appEnv,
		MongoDBURI:            getEnv("MONGODB_URI", getEnv("MONGO_URI", "")),
		MongoDBName:           getEnv("MONGODB_DB_NAME", getEnv("DB_NAME", "")),
		JWTSecret:             getEnv("JWT_SECRET", "your-super-secret-jwt-key-change-this-in-production"),
		BrevoAPIKey:           getEnv("BREVO_API_KEY", ""),
		TMDBBearerToken:       getEnv("TMDB_BEARER_TOKEN", ""),
		TMDBBaseURL:           tmdbBaseURL,
		OMDBAPIKey:            firstOMDBKey,
		OMDBAPIKeys:           omdbKeys,
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

// getOMDBAPIKeys discovers all configured OMDb API keys from environment variables.
// It supports:
//   - OMDB_API_KEYS (comma-separated list)
//   - OMDB_API_KEY (single or comma-separated list)
//   - OMDB_API_KEY1, OMDB_API_KEY2, OMDB_API_KEY_1, etc. (numbered/prefixed variables)
func getOMDBAPIKeys() []string {
	var keys []string
	seen := make(map[string]bool)

	addKey := func(k string) {
		k = strings.TrimSpace(k)
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	// 1. Check comma-separated OMDB_API_KEYS
	if envKeys := os.Getenv("OMDB_API_KEYS"); envKeys != "" {
		for _, part := range strings.Split(envKeys, ",") {
			addKey(part)
		}
	}

	// 2. Check standard OMDB_API_KEY (can also be comma-separated)
	if envKey := os.Getenv("OMDB_API_KEY"); envKey != "" {
		for _, part := range strings.Split(envKey, ",") {
			addKey(part)
		}
	}

	// 3. Scan all environment variables for keys starting with OMDB_API_KEY (e.g. OMDB_API_KEY1..5)
	type envPair struct {
		name string
		val  string
	}
	var omdbEnvPairs []envPair
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			keyName := strings.ToUpper(strings.TrimSpace(parts[0]))
			if strings.HasPrefix(keyName, "OMDB_API_KEY") && keyName != "OMDB_API_KEY" && keyName != "OMDB_API_KEYS" {
				omdbEnvPairs = append(omdbEnvPairs, envPair{name: keyName, val: parts[1]})
			}
		}
	}
	sort.Slice(omdbEnvPairs, func(i, j int) bool {
		return omdbEnvPairs[i].name < omdbEnvPairs[j].name
	})
	for _, pair := range omdbEnvPairs {
		addKey(pair.val)
	}

	return keys
}
