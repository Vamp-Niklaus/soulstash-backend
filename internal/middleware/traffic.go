package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/traffic"
)

var (
	mobileRegex = regexp.MustCompile(`(?i)mobile|android|iphone`)
	tabletRegex = regexp.MustCompile(`(?i)ipad|tablet`)
)

// TrafficLogging buffers observable traffic requests into the telemetry buffer
func TrafficLogging(buf *traffic.Buffer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if strings.HasPrefix(path, "/images") ||
				strings.HasPrefix(path, "/assets") ||
				strings.HasPrefix(path, "/js") ||
				strings.HasPrefix(path, "/favicon") ||
				strings.HasPrefix(path, "/api/admin/trafficLogs") {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			interceptor := &responseWriterInterceptor{ResponseWriter: w}

			cleanIP := GetClientIP(r)
			userAgent := r.Header.Get("User-Agent")

			device := "desktop"
			if mobileRegex.MatchString(userAgent) {
				device = "mobile"
			} else if tabletRegex.MatchString(userAgent) {
				device = "tablet"
			}

			username := extractUsernameForTraffic(r)
			domainName := r.Host
			if domainName == "" {
				domainName = "soulstash.onrender.com"
			}

			referer := r.Header.Get("Referer")
			if referer == "" {
				referer = r.Header.Get("Origin")
			}

			next.ServeHTTP(interceptor, r)

			logEntry := domain.TrafficLog{
				IP:             cleanIP,
				Domain:         domainName,
				Path:           path,
				Method:         r.Method,
				StatusCode:     interceptor.StatusCode(),
				ResponseTimeMs: time.Since(start).Milliseconds(),
				Username:       username,
				UserAgent:      userAgent,
				Device:         device,
				Referer:        referer,
				CreatedAt:      time.Now(),
			}

			if buf != nil {
				buf.Push(logEntry)
			}
		})
	}
}

func extractUsernameForTraffic(r *http.Request) string {
	if name := r.Header.Get("X-User-Name"); name != "" {
		return name
	}

	auth := r.Header.Get("Authorization")
	if auth != "" && strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[7:])
		parts := strings.Split(token, ".")
		if len(parts) >= 2 {
			payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err == nil {
				var claims map[string]any
				if err := json.Unmarshal(payloadBytes, &claims); err == nil {
					if uname, ok := claims["username"].(string); ok && uname != "" {
						return uname
					}
				}
			}
		}
	}

	return "anonymous"
}
