package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter creates a thread-safe sliding rate limiter for IP addresses
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiter
	r        rate.Limit
	burst    int
}

// NewRateLimiter creates a rate limiter matching Node: 1000 requests per 15 minutes
func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	r := rate.Every(window / time.Duration(maxRequests))
	rl := &RateLimiter{
		limiters: make(map[string]*ipLimiter),
		r:        r,
		burst:    maxRequests,
	}

	// Periodically clean up old limiters to prevent memory leaks
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			rl.mu.Lock()
			now := time.Now()
			for ip, entry := range rl.limiters {
				if now.Sub(entry.lastSeen) > 20*time.Minute {
					delete(rl.limiters, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()

	return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, exists := rl.limiters[ip]
	if !exists {
		entry = &ipLimiter{
			limiter:  rate.NewLimiter(rl.r, rl.burst),
			lastSeen: time.Now(),
		}
		rl.limiters[ip] = entry
		return entry.limiter
	}

	entry.lastSeen = time.Now()
	return entry.limiter
}

// Handler returns HTTP middleware enforcing the rate limit
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := GetClientIP(r)
		limiter := rl.getLimiter(ip)

		if !limiter.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "Too many requests, please try again later.",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// GetClientIP extracts user's real IP, handling X-Forwarded-For and IPv6 prefix
func GetClientIP(r *http.Request) string {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return cleanIP(ip)
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return cleanIP(host)
	}

	return cleanIP(r.RemoteAddr)
}

func cleanIP(ip string) string {
	cleaned := strings.TrimPrefix(ip, "::ffff:")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "127.0.0.1"
	}
	return cleaned
}
