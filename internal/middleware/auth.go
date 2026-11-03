package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"soulstash/internal/domain"

	"github.com/golang-jwt/jwt/v5"
)

// AuthenticateToken verifies Bearer JWT token; returns 401 if missing, 403 if invalid
func AuthenticateToken(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := extractBearerToken(r)
			if tokenString == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Access token required",
				})
				return
			}

			claims, err := parseToken(tokenString, jwtSecret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Invalid or expired token",
				})
				return
			}

			ctx := context.WithValue(r.Context(), UserKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OptionalAuth parses Bearer JWT if present, otherwise sets user as nil
func OptionalAuth(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString := extractBearerToken(r)
			if tokenString == "" {
				next.ServeHTTP(w, r)
				return
			}

			claims, err := parseToken(tokenString, jwtSecret)
			if err == nil && claims != nil {
				ctx := context.WithValue(r.Context(), UserKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GetUserFromContext retrieves authenticated UserClaims from context
func GetUserFromContext(ctx context.Context) *domain.UserClaims {
	if val, ok := ctx.Value(UserKey).(*domain.UserClaims); ok {
		return val
	}
	return nil
}

func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func parseToken(tokenString, secret string) (*domain.UserClaims, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, domain.ErrTokenExpired
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, domain.ErrTokenExpired
	}

	userClaims := &domain.UserClaims{}
	if uid, ok := claims["userId"].(string); ok {
		userClaims.UserID = uid
	}
	if uname, ok := claims["username"].(string); ok {
		userClaims.Username = uname
	}

	return userClaims, nil
}
