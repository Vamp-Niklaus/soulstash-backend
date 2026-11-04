package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/cors"
)

var (
	devLocalhostRegex = regexp.MustCompile(`^http://(?:localhost|127\.0\.0\.1):\d+$`)
	prodOrigins       = map[string]bool{
		"https://soulstash.onrender.com":     true,
		"https://app.soulstash.onrender.com": true,
		"http://localhost":                   true,
		"https://localhost":                  true,
		"capacitor://localhost":              true,
		"http://tauri.localhost":             true,
	}
)

// CORS returns CORS middleware configured with behavioral parity to Node.js
func CORS(isProduction bool) func(http.Handler) http.Handler {
	if isProduction {
		return cors.Handler(cors.Options{
			AllowedOrigins: []string{
				"https://soulstash.onrender.com",
				"https://app.soulstash.onrender.com",
				"http://localhost",
				"https://localhost",
				"capacitor://localhost",
				"http://tauri.localhost",
			},
			AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-User-Name", "X-Collection-Version"},
			ExposedHeaders:   []string{"Link", "X-Collection-Version", "X-Collection-Stale", "X-Request-Id"},
			AllowCredentials: true,
			MaxAge:           300,
		})
	}

	// Development mode: allow localhost:3000, 127.0.0.1:3000, and any localhost port
	return cors.Handler(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			lower := strings.ToLower(origin)
			if lower == "http://localhost:3000" || lower == "http://127.0.0.1:3000" {
				return true
			}
			return devLocalhostRegex.MatchString(lower)
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-User-Name", "X-Collection-Version"},
		ExposedHeaders:   []string{"Link", "X-Collection-Version", "X-Collection-Stale", "X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           300,
	})
}
