package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"soulstash/internal/domain"
	"soulstash/internal/middleware"
	"soulstash/internal/service"

	"github.com/go-chi/chi/v5"
)

// AuthHandler exposes HTTP handlers for authentication and user profiles
type AuthHandler struct {
	authService service.AuthService
	jwtSecret   string
}

// NewAuthHandler creates a new AuthHandler
func NewAuthHandler(authService service.AuthService, jwtSecret string) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		jwtSecret:   jwtSecret,
	}
}

// RegisterRoutes mounts all auth endpoints onto the given router
func (h *AuthHandler) RegisterRoutes(r chi.Router) {
	r.Get("/check-username", h.CheckUsername)
	r.Post("/register", h.Register)
	r.Post("/login", h.Login)
	r.Post("/logout", h.Logout)
	r.Post("/send-otp", h.SendOTP)
	r.Post("/forgot-password", h.ForgotPassword)
	r.Post("/reset-password", h.ResetPassword)
	r.Post("/verify-otp-and-register", h.VerifyOTPAndRegister)

	// Protected endpoints
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.AuthenticateToken(h.jwtSecret))
		protected.Post("/verify-token", h.VerifyToken)
		protected.Get("/me", h.GetMe)
		protected.Get("/profile", h.GetProfile)
		protected.Post("/update-profile", h.UpdateProfile)
	})
}

func (h *AuthHandler) CheckUsername(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	available, message, err := h.authService.CheckUsername(r.Context(), username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"available": false,
			"message":   "Server error",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available": available,
		"message":   message,
	})
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	res, err := h.authService.Register(r.Context(), body.Username, body.Password, body.FullName)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, res)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	res, err := h.authService.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Logout successful!",
	})
}

func (h *AuthHandler) VerifyToken(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	if userClaims == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	res, err := h.authService.VerifyToken(r.Context(), userClaims.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	if userClaims == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	user, err := h.authService.GetMe(r.Context(), userClaims.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	res := map[string]any{
		"id":        user.ID.Hex(),
		"username":  user.Username,
		"createdAt": user.CreatedAt,
		"admin":     user.Admin,
		"showAdult": user.ShowAdult,
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *AuthHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	if userClaims == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	user, err := h.authService.GetProfile(r.Context(), userClaims.UserID)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userClaims := middleware.GetUserFromContext(r.Context())
	if userClaims == nil {
		writeError(w, http.StatusUnauthorized, "Access token required")
		return
	}

	// Support both multipart/form-data (avatar file upload) and application/json
	update := service.ProfileUpdate{}
	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "multipart/form-data") {
		_ = r.ParseMultipartForm(5 * 1024 * 1024) // 5 MB max matching Node
		update.FirstName = r.FormValue("firstName")
		update.LastName = r.FormValue("lastName")
		update.DateOfBirth = r.FormValue("dateOfBirth")
		update.Bio = r.FormValue("bio")
		update.InstagramHandle = r.FormValue("instagramHandle")
		update.XHandle = r.FormValue("xHandle")
		update.YoutubeHandle = r.FormValue("youtubeHandle")

		file, header, err := r.FormFile("avatar")
		if err == nil && file != nil {
			defer file.Close()
			data, err := io.ReadAll(file)
			if err == nil && len(data) > 0 {
				mime := header.Header.Get("Content-Type")
				if mime == "" {
					mime = "image/png"
				}
				update.Avatar = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
		} else if avatarUrl := r.FormValue("avatarUrl"); avatarUrl != "" {
			update.Avatar = avatarUrl
		} else if avatar := r.FormValue("avatar"); avatar != "" {
			update.Avatar = avatar
		}
	} else {
		var body struct {
			FirstName       string `json:"firstName"`
			LastName        string `json:"lastName"`
			DateOfBirth     string `json:"dateOfBirth"`
			Bio             string `json:"bio"`
			InstagramHandle string `json:"instagramHandle"`
			XHandle         string `json:"xHandle"`
			YoutubeHandle   string `json:"youtubeHandle"`
			Avatar          string `json:"avatar"`
			AvatarUrl       string `json:"avatarUrl"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		update.FirstName = body.FirstName
		update.LastName = body.LastName
		update.DateOfBirth = body.DateOfBirth
		update.Bio = body.Bio
		update.InstagramHandle = body.InstagramHandle
		update.XHandle = body.XHandle
		update.YoutubeHandle = body.YoutubeHandle
		update.Avatar = body.AvatarUrl
		if update.Avatar == "" {
			update.Avatar = body.Avatar
		}
	}

	user, err := h.authService.UpdateProfile(r.Context(), userClaims.UserID, update)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *AuthHandler) SendOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	otp, err := h.authService.SendOTP(r.Context(), body.Email, body.Username, body.FullName)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	resp := map[string]any{"message": "OTP sent successfully"}
	// In dev mode, return OTP for easy testing
	if strings.EqualFold(r.Header.Get("X-Environment"), "development") || otp != "" {
		resp["otp"] = otp
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Email is required")
		return
	}

	if err := h.authService.ForgotPassword(r.Context(), body.Email); err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "OTP sent to your email!",
	})
}

func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		OTP         string `json:"otp"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Email, OTP, and new password are required")
		return
	}

	if err := h.authService.ResetPassword(r.Context(), body.Email, body.OTP, body.NewPassword); err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Password reset successfully!",
	})
}

func (h *AuthHandler) VerifyOTPAndRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
		OTP      string `json:"otp"`
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "All fields are required")
		return
	}

	res, err := h.authService.VerifyOTPAndRegister(r.Context(), body.Email, body.Username, body.Password, body.OTP, body.FullName)
	if err != nil {
		handleServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, res)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func handleServiceError(w http.ResponseWriter, err error) {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		writeError(w, appErr.Code, appErr.Message)
		return
	}

	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	if errors.Is(err, domain.ErrConflict) {
		writeError(w, http.StatusConflict, "Conflict")
		return
	}
	if errors.Is(err, domain.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if errors.Is(err, domain.ErrForbidden) {
		writeError(w, http.StatusForbidden, "Forbidden")
		return
	}

	writeError(w, http.StatusInternalServerError, "Something went wrong!")
}
