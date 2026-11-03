package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/middleware"
	"soulstash/internal/platform/mongodb"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/platform/traffic"
	"soulstash/internal/repository"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type AdminHandler struct {
	userRepo      repository.UserRepository
	adultEngine   *service.AdultDecisionEngine
	trafficBuffer *traffic.Buffer
	tmdbClient    tmdb.Client
	db            *mongo.Database
	jwtSecret     string
}

func NewAdminHandler(
	userRepo repository.UserRepository,
	adultEngine *service.AdultDecisionEngine,
	trafficBuf *traffic.Buffer,
	tmdbClient tmdb.Client,
	db *mongo.Database,
	jwtSecret string,
) *AdminHandler {
	return &AdminHandler{
		userRepo:      userRepo,
		adultEngine:   adultEngine,
		trafficBuffer: trafficBuf,
		tmdbClient:    tmdbClient,
		db:            db,
		jwtSecret:     jwtSecret,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	// Unprotected client batch log post matching Node
	r.Post("/trafficLogs", h.PostTrafficLogs)

	// Protected admin routes
	r.Group(func(admin chi.Router) {
		admin.Use(middleware.AuthenticateToken(h.jwtSecret))
		admin.Use(h.ensureAdmin)

		admin.Get("/users", h.GetUsers)
		admin.Get("/me", h.GetMe)
		admin.Post("/preferences", h.UpdatePreferences)
		admin.Post("/multimovies", h.UpdateMultimovies)
		admin.Get("/trafficLogs/stats", h.GetTrafficStats)
		admin.Get("/users/{username}/profile", h.GetUserProfile)
		admin.Post("/users/{username}/avatar", h.UpdateUserAvatar)

		// Adult criteria
		admin.Get("/adult-criteria", h.GetAdultCriteria)
		admin.Post("/adult-criteria", h.SaveAdultCriteria)
		admin.Post("/adult-criteria/reset", h.ResetAdultCriteria)
		admin.Post("/adult-criteria/test", h.TestAdultCriteria)
		admin.Get("/adult-criteria/import/{type}/{id}", h.ImportAdultMetadata)

		// Adult keywords
		admin.Get("/adult-keywords", h.GetAdultKeywords)
		admin.Post("/adult-keywords", h.SaveAdultKeywords)
		admin.Post("/adult-keywords/reset", h.ResetAdultKeywords)
	})
}

func (h *AdminHandler) ensureAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userClaims := middleware.GetUserFromContext(r.Context())
		if userClaims == nil {
			writeError(w, http.StatusUnauthorized, "Access token required")
			return
		}

		user, err := h.userRepo.FindByID(r.Context(), userClaims.UserID)
		if err != nil || user == nil || !user.Admin {
			writeError(w, http.StatusForbidden, "Admin access required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *AdminHandler) GetUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.userRepo.FindAllUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch admin user data")
		return
	}

	type FormattedUser struct {
		domain.User
		CollectionCount int `json:"collectionCount"`
		TotalSavedItems int `json:"totalSavedItems"`
		WatchedCount    int `json:"watchedCount"`
		WatchlistCount  int `json:"watchlistCount"`
	}

	formatted := make([]FormattedUser, len(users))
	for i, u := range users {
		watchedCount := 0
		watchlistCount := 0
		totalSaved := 0

		for _, c := range u.Collections {
			count := len(c.Movies)
			totalSaved += count
			if c.Name == "Watched" {
				watchedCount = count
			} else if c.Name == "Watchlist" {
				watchlistCount = count
			}
		}

		formatted[i] = FormattedUser{
			User:            u,
			CollectionCount: len(u.Collections),
			TotalSavedItems: totalSaved,
			WatchedCount:    watchedCount,
			WatchlistCount:  watchlistCount,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totalUsers": len(formatted),
		"users":      formatted,
	})
}

func (h *AdminHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	user, _ := h.userRepo.FindByID(r.Context(), userClaims.UserID)

	adminMode := 0
	showAdult := false
	if user != nil {
		adminMode = user.AdminMode
		showAdult = user.ShowAdult
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"admin":     true,
		"showAdult": showAdult,
		"adminMode": adminMode,
		"multimovies": map[string]any{
			"available": true,
			"rootUrls":  []string{"https://multimovies.fyi/"},
			"baseUrls":  []string{"https://multimovies.fyi/"},
		},
	})
}

func (h *AdminHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	var body struct {
		ShowAdult bool `json:"showAdult"`
		AdminMode int  `json:"adminMode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	_ = h.userRepo.UpdatePreferences(r.Context(), userClaims.UserID, body.ShowAdult, body.AdminMode)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"showAdult": body.ShowAdult,
		"adminMode": body.AdminMode,
	})
}

func (h *AdminHandler) UpdateMultimovies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RootURL string `json:"rootUrl"`
		BaseURL string `json:"baseUrl"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"multimovies": map[string]any{
			"available": true,
			"rootUrls":  []string{body.RootURL, "https://multimovies.fyi/"},
			"baseUrls":  []string{body.BaseURL, "https://multimovies.fyi/"},
		},
	})
}

func (h *AdminHandler) PostTrafficLogs(w http.ResponseWriter, r *http.Request) {
	var logs []domain.TrafficLog
	if err := json.NewDecoder(r.Body).Decode(&logs); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}

	if h.trafficBuffer != nil {
		for _, l := range logs {
			h.trafficBuffer.Push(l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": len(logs)})
}

func (h *AdminHandler) GetTrafficStats(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"totalRequests": 0,
			"requestTypes":  []any{},
			"locations":     []any{},
			"timeSeries":    []any{},
		})
		return
	}

	coll := h.db.Collection(mongodb.CollectionTrafficLogs)
	count, _ := coll.CountDocuments(r.Context(), bson.M{})

	writeJSON(w, http.StatusOK, map[string]any{
		"totalRequests": count,
		"requestTypes":  []any{},
		"locations":     []any{},
		"timeSeries":    []any{},
	})
}

func (h *AdminHandler) GetUserProfile(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	user, err := h.userRepo.FindByUsername(r.Context(), username)
	if err != nil {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":         user,
		"isOwner":      false,
		"isAdminView":  true,
		"isFollowing":  false,
		"isFollowedBy": false,
	})
}

func (h *AdminHandler) UpdateUserAvatar(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	avatar := ""

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(5 * 1024 * 1024)
		file, header, err := r.FormFile("avatar")
		if err == nil && file != nil {
			defer file.Close()
			data, err := io.ReadAll(file)
			if err == nil {
				mime := header.Header.Get("Content-Type")
				if mime == "" {
					mime = "image/png"
				}
				avatar = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
		} else {
			avatar = r.FormValue("avatarUrl")
		}
	} else {
		var body struct {
			AvatarUrl string `json:"avatarUrl"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		avatar = body.AvatarUrl
	}

	_ = h.userRepo.Update(r.Context(), username, bson.M{
		"$set": bson.M{"avatar": avatar, "updatedAt": time.Now()},
	})

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "avatar": avatar})
}

func (h *AdminHandler) GetAdultCriteria(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tree":        h.adultEngine.GetTree(),
		"defaultTree": service.GetDefaultTree(),
	})
}

func (h *AdminHandler) SaveAdultCriteria(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tree *service.TreeNode `json:"tree"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tree == nil {
		writeError(w, http.StatusBadRequest, "Decision tree is required")
		return
	}

	h.adultEngine.SetTree(body.Tree)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tree": body.Tree})
}

func (h *AdminHandler) ResetAdultCriteria(w http.ResponseWriter, r *http.Request) {
	tree := h.adultEngine.ResetTree()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tree": tree})
}

func (h *AdminHandler) TestAdultCriteria(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tree *service.TreeNode `json:"tree"`
		Item map[string]any    `json:"item"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	isAdult, trace := h.adultEngine.Test(body.Tree, body.Item)
	writeJSON(w, http.StatusOK, map[string]any{"isAdult": isAdult, "trace": trace})
}

func (h *AdminHandler) ImportAdultMetadata(w http.ResponseWriter, r *http.Request) {
	rawType := chi.URLParam(r, "type")
	id := chi.URLParam(r, "id")
	tmdbType := "movie"
	if rawType == "tv" || rawType == "series" {
		tmdbType = "tv"
	}

	data, err := h.tmdbClient.Get(r.Context(), fmt.Sprintf("/3/%s/%s", tmdbType, id), map[string]string{"language": "en-US"})
	if err != nil {
		writeError(w, http.StatusNotFound, "Content not found on TMDB")
		return
	}

	title, _ := data["title"].(string)
	if title == "" {
		title, _ = data["name"].(string)
	}
	relDate, _ := data["release_date"].(string)
	if relDate == "" {
		relDate, _ = data["first_air_date"].(string)
	}

	importedItem := map[string]any{
		"id":                    data["id"],
		"title":                 title,
		"adult":                 data["adult"] == true,
		"vote_count":            data["vote_count"],
		"release_date":          relDate,
		"imdb_id":               data["imdb_id"],
		"origin_country":        data["origin_country"],
		"original_language":     data["original_language"],
		"overview":              data["overview"],
		"genres":                data["genres"],
		"is_bollywood_endpoint": false,
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": importedItem})
}

func (h *AdminHandler) GetAdultKeywords(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"keywords": h.adultEngine.GetKeywords(),
	})
}

func (h *AdminHandler) SaveAdultKeywords(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keywords []service.KeywordItem `json:"keywords"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Keywords array required")
		return
	}

	h.adultEngine.SetKeywords(body.Keywords)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "keywords": body.Keywords})
}

func (h *AdminHandler) ResetAdultKeywords(w http.ResponseWriter, r *http.Request) {
	keywords := h.adultEngine.ResetKeywords()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "keywords": keywords})
}
