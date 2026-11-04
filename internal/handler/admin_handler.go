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
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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
	if userClaims == nil || h.userRepo == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}
	user, err := h.userRepo.FindByID(r.Context(), userClaims.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch admin info")
		return
	}

	adminMode := 0
	showAdult := false
	if user != nil {
		adminMode = user.AdminMode
		showAdult = user.ShowAdult
	}

	multimovies := map[string]any{
		"available": true,
		"rootUrls":  []string{"https://multimovies.fyi/"},
		"baseUrls":  []string{"https://multimovies.fyi/"},
	}
	if h.db != nil {
		var config struct {
			Available bool     `bson:"available"`
			RootURLs  []string `bson:"rootUrls"`
			BaseURLs  []string `bson:"baseUrls"`
			UpdatedAt any      `bson:"updatedAt"`
		}
		if err := h.db.Collection(mongodb.CollectionSourceConfigs).FindOne(r.Context(), bson.M{"_id": mongodb.DocIDMultimovies}).Decode(&config); err == nil {
			multimovies = map[string]any{
				"available": config.Available,
				"rootUrls":  normalizeAdminURLList(config.RootURLs, "https://multimovies.fyi/"),
				"baseUrls":  normalizeAdminURLList(config.BaseURLs, "https://multimovies.fyi/"),
				"updatedAt": config.UpdatedAt,
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":       true,
		"showAdult":   showAdult,
		"adminMode":   adminMode,
		"multimovies": multimovies,
	})
}

func (h *AdminHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	var body struct {
		ShowAdult bool `json:"showAdult"`
		AdminMode *int `json:"adminMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid preferences")
		return
	}
	mode := 0
	if body.AdminMode != nil {
		mode = *body.AdminMode
	} else if body.ShowAdult {
		mode = 1
	}
	showAdult := mode == 1
	if err := h.userRepo.UpdatePreferences(r.Context(), userClaims.UserID, showAdult, mode); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update admin preferences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"showAdult": showAdult,
		"adminMode": mode,
	})
}

func (h *AdminHandler) UpdateMultimovies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RootURL string `json:"rootUrl"`
		BaseURL string `json:"baseUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid Multimovies configuration")
		return
	}
	currentRootURLs := []string{"https://multimovies.fyi/"}
	currentBaseURLs := []string{"https://multimovies.fyi/"}
	if h.db != nil {
		var current struct {
			RootURLs []string `bson:"rootUrls"`
			BaseURLs []string `bson:"baseUrls"`
		}
		_ = h.db.Collection(mongodb.CollectionSourceConfigs).FindOne(r.Context(), bson.M{"_id": mongodb.DocIDMultimovies}).Decode(&current)
		currentRootURLs = normalizeAdminURLList(current.RootURLs, currentRootURLs[0])
		currentBaseURLs = normalizeAdminURLList(current.BaseURLs, currentBaseURLs[0])
		currentRootURLs = prependAdminURL(currentRootURLs, body.RootURL)
		currentBaseURLs = prependAdminURL(currentBaseURLs, body.BaseURL)
		_, err := h.db.Collection(mongodb.CollectionSourceConfigs).UpdateOne(r.Context(), bson.M{"_id": mongodb.DocIDMultimovies}, bson.M{
			"$set": bson.M{
				"className": "multimovies", "available": true,
				"rootUrls": currentRootURLs, "baseUrls": currentBaseURLs, "updatedAt": time.Now(),
			},
			"$setOnInsert": bson.M{"createdAt": time.Now()},
		}, options.UpdateOne().SetUpsert(true))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to update Multimovies config")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"multimovies": map[string]any{
			"available": true,
			"rootUrls":  currentRootURLs,
			"baseUrls":  currentBaseURLs,
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
	filter := bson.M{}
	if ip := strings.TrimSpace(r.URL.Query().Get("ip")); ip != "" {
		filter["ip"] = bson.M{"$regex": ip, "$options": "i"}
	}
	count, err := coll.CountDocuments(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch traffic statistics")
		return
	}
	match := bson.D{{Key: "$match", Value: filter}}
	requestTypes, err := aggregateAdminData(r, coll, mongo.Pipeline{
		match,
		bson.D{{Key: "$group", Value: bson.M{"_id": "$method", "count": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch traffic statistics")
		return
	}
	locationsData, err := aggregateAdminData(r, coll, mongo.Pipeline{
		match,
		bson.D{{Key: "$group", Value: bson.M{
			"_id": "$ip", "count": bson.M{"$sum": 1}, "city": bson.M{"$first": "$city"},
			"country": bson.M{"$first": "$country"}, "lastActive": bson.M{"$max": "$createdAt"},
			"usernames": bson.M{"$addToSet": "$username"},
		}}},
		bson.D{{Key: "$sort", Value: bson.M{"count": -1}}}, bson.D{{Key: "$limit", Value: 50}},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch traffic statistics")
		return
	}
	timeSeriesData, err := aggregateAdminData(r, coll, mongo.Pipeline{
		match,
		bson.D{{Key: "$group", Value: bson.M{"_id": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$createdAt"}}, "count": bson.M{"$sum": 1}}}},
		bson.D{{Key: "$sort", Value: bson.M{"_id": 1}}},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to fetch traffic statistics")
		return
	}
	types := make([]map[string]any, 0, len(requestTypes))
	for _, item := range requestTypes {
		method, _ := item["_id"].(string)
		if method == "" {
			method = "UNKNOWN"
		}
		types = append(types, map[string]any{"type": method, "count": item["count"]})
	}
	locations := make([]map[string]any, 0, len(locationsData))
	for _, item := range locationsData {
		ip, _ := item["_id"].(string)
		city := adminStringOr(item["city"], "Unknown")
		country := adminStringOr(item["country"], "Unknown")
		if isLocalAdminIP(ip) {
			city, country = "Localhost", "Local"
		}
		users := make([]string, 0)
		if rawUsers, ok := item["usernames"].([]any); ok {
			for _, rawUser := range rawUsers {
				if username, ok := rawUser.(string); ok && username != "" && username != "anonymous" {
					users = append(users, username)
				}
			}
		}
		var userValue any
		if len(users) > 0 {
			userValue = users
		}
		locations = append(locations, map[string]any{
			"ip": adminStringOr(item["_id"], "Unknown"), "city": city, "country": country,
			"count": item["count"], "lastActive": item["lastActive"], "users": userValue,
		})
	}
	timeSeries := make([]map[string]any, 0, len(timeSeriesData))
	for _, item := range timeSeriesData {
		timeSeries = append(timeSeries, map[string]any{"time": item["_id"], "count": item["count"]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totalRequests": count, "requestTypes": types, "locations": locations, "timeSeries": timeSeries,
	})
}

func (h *AdminHandler) GetUserProfile(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	user, err := h.userRepo.FindByUsername(r.Context(), username)
	if err != nil {
		if err == domain.ErrNotFound {
			writeError(w, http.StatusNotFound, "User not found")
		} else {
			writeError(w, http.StatusInternalServerError, "Failed to fetch user profile")
		}
		return
	}
	if user.Collections == nil {
		user.Collections = []domain.UserCollection{}
	}
	if user.FavoritePeople == nil {
		user.FavoritePeople = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"_id": user.ID.Hex(), "username": user.Username, "email": user.Email, "fullName": user.FullName,
			"firstName": user.FirstName, "lastName": user.LastName, "dateOfBirth": user.DateOfBirth,
			"bio": user.Bio, "instagramHandle": user.InstagramHandle, "xHandle": user.XHandle,
			"youtubeHandle": user.YoutubeHandle, "avatar": user.Avatar, "admin": user.Admin,
			"adminMode": user.AdminMode, "showAdult": user.ShowAdult, "collections": user.Collections,
			"favoritePeople": user.FavoritePeople, "favoritePeoplePublic": user.FavoritePeoplePublic,
			"followers": user.Followers, "following": user.Following,
			"followersCount": len(user.Followers), "followingCount": len(user.Following),
			"createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt,
		},
		"isOwner":      false,
		"isAdminView":  true,
		"isFollowing":  false,
		"isFollowedBy": false,
	})
}

func (h *AdminHandler) UpdateUserAvatar(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	user, err := h.userRepo.FindByUsername(r.Context(), username)
	if err != nil {
		if err == domain.ErrNotFound {
			writeError(w, http.StatusNotFound, "User not found")
		} else {
			writeError(w, http.StatusInternalServerError, "Failed to update avatar")
		}
		return
	}
	avatar := user.Avatar

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
			if nextAvatar := r.FormValue("avatarUrl"); nextAvatar != "" {
				avatar = nextAvatar
			}
		}
	} else {
		var body struct {
			AvatarUrl string `json:"avatarUrl"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		avatar = body.AvatarUrl
	}

	if err := h.userRepo.Update(r.Context(), username, bson.M{
		"$set": bson.M{"avatar": avatar, "updatedAt": time.Now()},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update avatar")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "avatar": avatar})
}

func (h *AdminHandler) GetAdultCriteria(w http.ResponseWriter, r *http.Request) {
	tree := service.GetDefaultTree()
	if h.db != nil {
		var stored struct {
			Tree *service.TreeNode `bson:"tree"`
		}
		err := h.db.Collection(mongodb.CollectionAdminSettings).FindOne(r.Context(), bson.M{"_id": "adult_criteria"}).Decode(&stored)
		if err == nil && stored.Tree != nil {
			tree = stored.Tree
		} else if err != nil && err != mongo.ErrNoDocuments {
			writeError(w, http.StatusInternalServerError, "Failed to load adult criteria")
			return
		}
	}
	if h.adultEngine != nil {
		h.adultEngine.SetTree(tree)
		tree = h.adultEngine.GetTree()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tree":        tree,
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

	if h.adultEngine != nil {
		h.adultEngine.SetTree(body.Tree)
	}
	if err := h.saveAdminSetting(r, "adult_criteria", "tree", body.Tree); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save adult criteria")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tree": body.Tree})
}

func (h *AdminHandler) ResetAdultCriteria(w http.ResponseWriter, r *http.Request) {
	tree := service.GetDefaultTree()
	if h.adultEngine != nil {
		tree = h.adultEngine.ResetTree()
	}
	if err := h.saveAdminSetting(r, "adult_criteria", "tree", tree); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to reset adult criteria")
		return
	}
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
		"origin_country":        adminOriginCountries(data),
		"original_language":     data["original_language"],
		"overview":              data["overview"],
		"genres":                data["genres"],
		"is_bollywood_endpoint": false,
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": importedItem})
}

func (h *AdminHandler) GetAdultKeywords(w http.ResponseWriter, r *http.Request) {
	keywords := service.GetDefaultKeywords()
	if h.db != nil {
		var stored struct {
			Keywords []service.KeywordItem `bson:"keywords"`
		}
		err := h.db.Collection(mongodb.CollectionAdminSettings).FindOne(r.Context(), bson.M{"_id": "adult_keywords_config"}).Decode(&stored)
		if err == nil && len(stored.Keywords) > 0 {
			keywords = stored.Keywords
		} else if err != nil && err != mongo.ErrNoDocuments {
			writeError(w, http.StatusInternalServerError, "Failed to load adult keywords")
			return
		}
	}
	if h.adultEngine != nil {
		h.adultEngine.SetKeywords(keywords)
		keywords = h.adultEngine.GetKeywords()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"keywords":        keywords,
		"defaultKeywords": service.GetDefaultKeywords(),
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

	keywords := make([]service.KeywordItem, 0, len(body.Keywords))
	for _, item := range body.Keywords {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		if item.ID != "" || item.Name != "" {
			keywords = append(keywords, item)
		}
	}
	if h.adultEngine != nil {
		h.adultEngine.SetKeywords(keywords)
	}
	if err := h.saveAdminSetting(r, "adult_keywords_config", "keywords", keywords); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save adult keywords")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "keywords": keywords})
}

func (h *AdminHandler) ResetAdultKeywords(w http.ResponseWriter, r *http.Request) {
	keywords := service.GetDefaultKeywords()
	if h.adultEngine != nil {
		keywords = h.adultEngine.ResetKeywords()
	}
	if err := h.saveAdminSetting(r, "adult_keywords_config", "keywords", keywords); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to reset adult keywords")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "keywords": keywords})
}

func (h *AdminHandler) saveAdminSetting(r *http.Request, id, field string, value any) error {
	if h.db == nil {
		return nil
	}
	_, err := h.db.Collection(mongodb.CollectionAdminSettings).UpdateOne(r.Context(), bson.M{"_id": id}, bson.M{
		"$set": bson.M{field: value, "updatedAt": time.Now()},
	}, options.UpdateOne().SetUpsert(true))
	return err
}

func normalizeAdminURLList(values []string, fallback string) []string {
	result := make([]string, 0, len(values)+1)
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 && fallback != "" {
		result = append(result, fallback)
	}
	return result
}

func prependAdminURL(values []string, next string) []string {
	next = strings.TrimSpace(next)
	values = normalizeAdminURLList(values, "")
	if next == "" || (len(values) > 0 && values[0] == next) {
		return values
	}
	return append([]string{next}, values...)
}

func aggregateAdminData(r *http.Request, collection *mongo.Collection, pipeline mongo.Pipeline) ([]bson.M, error) {
	cursor, err := collection.Aggregate(r.Context(), pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(r.Context())
	var results []bson.M
	if err := cursor.All(r.Context(), &results); err != nil {
		return nil, err
	}
	return results, nil
}

func adminStringOr(value any, fallback string) string {
	if str, ok := value.(string); ok && strings.TrimSpace(str) != "" {
		return str
	}
	return fallback
}

func isLocalAdminIP(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.")
}

func adminOriginCountries(data map[string]any) []any {
	if countries, ok := data["origin_country"].([]any); ok {
		return countries
	}
	productionCountries, _ := data["production_countries"].([]any)
	countries := make([]any, 0, len(productionCountries))
	for _, raw := range productionCountries {
		if item, ok := raw.(map[string]any); ok {
			if code, ok := item["iso_3166_1"].(string); ok && code != "" {
				countries = append(countries, code)
			}
		}
	}
	return countries
}
