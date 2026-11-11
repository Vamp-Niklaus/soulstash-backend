package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"soulstash/internal/domain"
	"soulstash/internal/middleware"
	"soulstash/internal/repository"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
)

type CollectionHandler struct {
	svc       service.CollectionService
	jwtSecret string
	userRepo  repository.UserRepository
}

func NewCollectionHandler(svc service.CollectionService, jwtSecret string, userRepos ...repository.UserRepository) *CollectionHandler {
	handler := &CollectionHandler{svc: svc, jwtSecret: jwtSecret}
	if len(userRepos) > 0 {
		handler.userRepo = userRepos[0]
	}
	return handler
}

func (h *CollectionHandler) RegisterRoutes(r chi.Router) {
	// Protected collection endpoints
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.AuthenticateToken(h.jwtSecret))

		protected.Get("/user/collections", h.GetUserCollections)
		protected.Post("/user/collections", h.CreateCollection)
		protected.Put("/user/collections/{id}", h.UpdateCollection)
		protected.Delete("/user/collections/{id}", h.DeleteCollection)
		protected.Post("/user/collections/{id}/add", h.AddToCollection)
		protected.Post("/user/collections/{id}/remove", h.RemoveFromCollection)
		protected.Get("/user/collections/{id}/movies", h.GetCollectionMovies)
		protected.Post("/user/collections/{id}/publish", h.PublishCollection)
		protected.Post("/user/collections/reorder", h.ReorderCollections)
		protected.Post("/user/collections/{id}/enrich-metadata", h.EnrichMetadata)
		protected.Post("/content/anime-detection", h.DetectAnime)

		// Social & favorites
		protected.Post("/user/follow", h.Follow)
		protected.Post("/user/unfollow", h.Unfollow)
		protected.Get("/user/favorites", h.GetFavorites)
		protected.Post("/user/favorites/add", h.AddFavorite)
		protected.Post("/user/favorites/remove", h.RemoveFavorite)
		protected.Post("/user/favorites/privacy", h.SetFavoritesPrivacy)
	})

	// Public social & published collections
	r.Group(func(public chi.Router) {
		public.Use(middleware.OptionalAuth(h.jwtSecret))
		public.Get("/user/profile/{username}", h.GetPublicProfile)
		public.Get("/user/{username}/followers", h.GetFollowers)
		public.Get("/user/{username}/following", h.GetFollowing)
		public.Get("/collection/{username}/{collectionName}", h.GetPublicCollection)
		public.Get("/public-collection/{username}/{collectionName}", h.GetPublicCollection)
		public.Get("/collections/published", h.GetPublishedCollections)
	})
}

// RegisterUserRoutes mounts routes directly onto a /user subrouter
func (h *CollectionHandler) RegisterUserRoutes(userR chi.Router) {
	userR.Group(func(protected chi.Router) {
		protected.Use(middleware.AuthenticateToken(h.jwtSecret))

		protected.Get("/collections", h.GetUserCollections)
		protected.Post("/collections", h.CreateCollection)
		protected.Put("/collections/{id}", h.UpdateCollection)
		protected.Delete("/collections/{id}", h.DeleteCollection)
		protected.Post("/collections/{id}/add", h.AddToCollection)
		protected.Post("/collections/{id}/remove", h.RemoveFromCollection)
		protected.Get("/collections/{id}/movies", h.GetCollectionMovies)
		protected.Post("/collections/{id}/publish", h.PublishCollection)
		protected.Post("/collections/reorder", h.ReorderCollections)
		protected.Post("/collections/{id}/enrich-metadata", h.EnrichMetadata)

		// Social & favorites
		protected.Post("/follow", h.Follow)
		protected.Post("/unfollow", h.Unfollow)
		protected.Get("/favorites", h.GetFavorites)
		protected.Post("/favorites/add", h.AddFavorite)
		protected.Post("/favorites/remove", h.RemoveFavorite)
		protected.Post("/favorites/privacy", h.SetFavoritesPrivacy)
	})

	userR.Group(func(public chi.Router) {
		public.Use(middleware.OptionalAuth(h.jwtSecret))
		public.Get("/profile/{username}", h.GetPublicProfile)
		public.Get("/{username}/followers", h.GetFollowers)
		public.Get("/{username}/following", h.GetFollowing)
	})
}

func (h *CollectionHandler) GetUserCollections(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	cols, version, err := h.svc.GetCollections(r.Context(), user.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	w.Header().Set("X-Collection-Version", strconv.Itoa(version))
	writeJSON(w, http.StatusOK, cols)
}

func (h *CollectionHandler) CreateCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IsPublic    bool   `json:"isPublic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	clientVer := getClientCollectionVersion(r)
	res, err := h.svc.CreateCollection(r.Context(), user.UserID, body.Name, body.Description, body.IsPublic, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusCreated, res)
}

func (h *CollectionHandler) UpdateCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Banner      *string `json:"banner"`
		IsPublic    *bool   `json:"isPublic"`
		Movies      *[]any  `json:"movies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	clientVer := getClientCollectionVersion(r)
	input := service.UpdateCollectionInput{
		Name:        body.Name,
		Description: body.Description,
		Banner:      body.Banner,
		IsPublic:    body.IsPublic,
		Movies:      body.Movies,
	}

	res, err := h.svc.UpdateCollection(r.Context(), user.UserID, colID, input, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	clientVer := getClientCollectionVersion(r)

	res, err := h.svc.DeleteCollection(r.Context(), user.UserID, colID, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) AddToCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	var movie map[string]any
	if err := json.NewDecoder(r.Body).Decode(&movie); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	clientVer := getClientCollectionVersion(r)
	res, err := h.svc.AddMovie(r.Context(), user.UserID, colID, movie, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) RemoveFromCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	var body struct {
		MovieID  any `json:"movieId"`
		SeriesID any `json:"seriesId"`
		ID       any `json:"id"`
		MediaID  any `json:"mediaId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var targetID any = body.MovieID
	if targetID == nil {
		targetID = body.SeriesID
	}
	if targetID == nil {
		targetID = body.ID
	}
	if targetID == nil {
		targetID = body.MediaID
	}

	clientVer := getClientCollectionVersion(r)
	res, err := h.svc.RemoveMovie(r.Context(), user.UserID, colID, targetID, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) GetCollectionMovies(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	movies, err := h.svc.GetCollectionMovies(r.Context(), user.UserID, colID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, movies)
}

func (h *CollectionHandler) PublishCollection(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	colID := chi.URLParam(r, "id")
	var body struct {
		Publish bool `json:"publish"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	clientVer := getClientCollectionVersion(r)
	res, err := h.svc.PublishCollection(r.Context(), user.UserID, colID, body.Publish, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) ReorderCollections(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var body struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	clientVer := getClientCollectionVersion(r)
	res, err := h.svc.ReorderCollections(r.Context(), user.UserID, body.Order, clientVer)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	setCollectionHeaders(w, res.CollectionVersion, res.StaleResolved)
	writeJSON(w, http.StatusOK, res)
}

func (h *CollectionHandler) EnrichMetadata(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	cols, ver, err := h.svc.GetCollections(r.Context(), user.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":           "Collection metadata updated",
		"collectionVersion": ver,
		"staleResolved":     false,
		"collections":       cols,
	})
}

func (h *CollectionHandler) DetectAnime(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	results := h.svc.DetectAnime(r.Context(), body.Items)
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (h *CollectionHandler) GetPublicProfile(w http.ResponseWriter, r *http.Request) {
	targetUsername := chi.URLParam(r, "username")
	currentUsername := ""
	if user := middleware.GetUserFromContext(r.Context()); user != nil {
		currentUsername = user.Username
	}

	profile, err := h.svc.GetPublicProfile(r.Context(), currentUsername, targetUsername)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, profile)
}

func (h *CollectionHandler) Follow(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var body struct {
		TargetUsername string `json:"targetUsername"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TargetUsername == "" {
		writeError(w, http.StatusBadRequest, "Target username is required")
		return
	}

	if err := h.svc.Follow(r.Context(), user.Username, body.TargetUsername); err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": fmt.Sprintf("You are now following %s", body.TargetUsername),
	})
}

func (h *CollectionHandler) Unfollow(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var body struct {
		TargetUsername string `json:"targetUsername"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TargetUsername == "" {
		writeError(w, http.StatusBadRequest, "Target username is required")
		return
	}

	if err := h.svc.Unfollow(r.Context(), user.Username, body.TargetUsername); err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": fmt.Sprintf("You unfollowed %s", body.TargetUsername),
	})
}

func (h *CollectionHandler) GetFollowers(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	followers, err := h.svc.GetFollowers(r.Context(), username)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	var users []map[string]any
	for _, uname := range followers {
		u, _ := h.svc.GetPublicProfile(r.Context(), "", uname)
		if u != nil {
			if userMap, ok := u["user"].(map[string]any); ok {
				users = append(users, map[string]any{
					"username": uname,
					"fullName": userMap["fullName"],
					"avatar":   userMap["avatar"],
					"bio":      userMap["bio"],
				})
				continue
			}
		}
		users = append(users, map[string]any{
			"username": uname,
			"fullName": uname,
			"avatar":   "",
			"bio":      "",
		})
	}
	if users == nil {
		users = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *CollectionHandler) GetFollowing(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	following, err := h.svc.GetFollowing(r.Context(), username)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	var users []map[string]any
	for _, uname := range following {
		u, _ := h.svc.GetPublicProfile(r.Context(), "", uname)
		if u != nil {
			if userMap, ok := u["user"].(map[string]any); ok {
				users = append(users, map[string]any{
					"username": uname,
					"fullName": userMap["fullName"],
					"avatar":   userMap["avatar"],
					"bio":      userMap["bio"],
				})
				continue
			}
		}
		users = append(users, map[string]any{
			"username": uname,
			"fullName": uname,
			"avatar":   "",
			"bio":      "",
		})
	}
	if users == nil {
		users = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *CollectionHandler) GetFavorites(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	favs, err := h.svc.GetFavorites(r.Context(), user.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorites": favs})
}

func (h *CollectionHandler) AddFavorite(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var person map[string]any
	if err := json.NewDecoder(r.Body).Decode(&person); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.svc.AddFavorite(r.Context(), user.UserID, person); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Person added to favorites"})
}

func (h *CollectionHandler) RemoveFavorite(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var personID int
	if v, ok := raw["personId"]; ok {
		personID = int(getFloat(v))
	}
	if personID == 0 {
		if v, ok := raw["id"]; ok {
			personID = int(getFloat(v))
		}
	}
	if personID == 0 {
		writeError(w, http.StatusBadRequest, "Valid person ID required")
		return
	}

	if err := h.svc.RemoveFavorite(r.Context(), user.UserID, personID); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Person removed from favorites"})
}

func (h *CollectionHandler) SetFavoritesPrivacy(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	var body struct {
		IsPublic bool `json:"isPublic"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.svc.SetFavoritesPrivacy(r.Context(), user.UserID, body.IsPublic); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Favorites privacy updated"})
}

func (h *CollectionHandler) GetPublicCollection(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	collectionName := chi.URLParam(r, "collectionName")

	col, err := h.svc.GetPublicCollection(r.Context(), username, collectionName)
	if err != nil && h.isAdminRequest(r) {
		if adminReader, ok := h.svc.(interface {
			GetCollectionForAdmin(context.Context, string, string) (*domain.UserCollection, error)
		}); ok {
			if adminCollection, adminErr := adminReader.GetCollectionForAdmin(r.Context(), username, collectionName); adminErr == nil {
				col, err = adminCollection, nil
			}
		}
	}
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"_id":         collectionResponseID(col),
		"name":        col.Name,
		"isDeletable": col.IsDeletable,
		"isPublic":    col.IsPublic,
		"isPublished": col.IsPublished,
		"description": col.Description,
		"banner":      collectionResponseBanner(col.Banner),
		"movieCount":  len(col.Movies),
		"movies":      collectionResponseMovies(col.Movies),
		"createdAt":   col.CreatedAt,
		"updatedAt":   col.UpdatedAt,
	}})
}

func (h *CollectionHandler) isAdminRequest(r *http.Request) bool {
	claims := middleware.GetUserFromContext(r.Context())
	if claims == nil || h.userRepo == nil {
		return false
	}
	user, err := h.userRepo.FindByID(r.Context(), claims.UserID)
	return err == nil && user != nil && user.Admin
}

func collectionResponseID(col *domain.UserCollection) any {
	if col.ID == nil || col.ID == "" {
		return col.Name
	}
	return col.ID
}

func collectionResponseBanner(banner string) string {
	if banner == "" {
		return "https://cdn.imgchest.com/files/b23d0bfcaa8b.jpg"
	}
	return banner
}

func collectionResponseMovies(movies []any) []any {
	if movies == nil {
		return []any{}
	}
	return movies
}

func (h *CollectionHandler) GetPublishedCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := h.svc.GetPublishedCollections(r.Context())
	if err != nil {
		handleServiceError(w, err)
		return
	}
	// Optional ?q= filters by collection name, description or publisher username.
	// Without q the response is unchanged, so the home page keeps working.
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := make([]map[string]any, 0, len(cols))
		for _, entry := range cols {
			if publishedEntryMatches(entry, q) {
				filtered = append(filtered, entry)
			}
		}
		cols = filtered
	}
	writeJSON(w, http.StatusOK, cols)
}

func publishedEntryMatches(entry map[string]any, q string) bool {
	username, _ := entry["username"].(string)
	col, _ := entry["collection"].(map[string]any)
	name, _ := col["name"].(string)
	desc, _ := col["description"].(string)
	return strings.Contains(strings.ToLower(name), q) ||
		strings.Contains(strings.ToLower(desc), q) ||
		strings.Contains(strings.ToLower(username), q)
}


func getClientCollectionVersion(r *http.Request) *int {
	raw := r.Header.Get("X-Collection-Version")
	if raw == "" {
		return nil
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &val
}

func setCollectionHeaders(w http.ResponseWriter, version int, stale bool) {
	w.Header().Set("X-Collection-Version", strconv.Itoa(version))
	if stale {
		w.Header().Set("X-Collection-Stale", "true")
	}
}
