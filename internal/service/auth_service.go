package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/email"
	"soulstash/internal/repository"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// AuthService coordinates authentication and user lifecycle
type AuthService interface {
	CheckUsername(ctx context.Context, username string) (bool, string, error)
	Register(ctx context.Context, username, password, fullName string) (*AuthResult, error)
	Login(ctx context.Context, identifier, password string) (*AuthResult, error)
	VerifyToken(ctx context.Context, userID string) (*TokenVerifyResult, error)
	GetMe(ctx context.Context, userID string) (*domain.User, error)
	GetProfile(ctx context.Context, userID string) (*domain.User, error)
	UpdateProfile(ctx context.Context, userID string, update ProfileUpdate) (*domain.User, error)
	SendOTP(ctx context.Context, email, username, fullName string) (string, error)
	ForgotPassword(ctx context.Context, emailAddress string) error
	ResetPassword(ctx context.Context, emailAddress, otp, newPassword string) error
	VerifyOTPAndRegister(ctx context.Context, email, username, password, otp, fullName string) (*AuthResult, error)
}

type AuthResult struct {
	Message string `json:"message"`
	Token   string `json:"token"`
	User    struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		FullName  string `json:"fullName,omitempty"`
		Email     string `json:"email,omitempty"`
		Admin     bool   `json:"admin"`
		ShowAdult bool   `json:"showAdult"`
	} `json:"user"`
}

type TokenVerifyResult struct {
	Success   bool   `json:"success"`
	Username  string `json:"username"`
	Admin     bool   `json:"admin"`
	ShowAdult bool   `json:"showAdult"`
}

type ProfileUpdate struct {
	FirstName       string
	LastName        string
	DateOfBirth     string
	Bio             string
	InstagramHandle string
	XHandle         string
	YoutubeHandle   string
	Avatar          string
}

type otpEntry struct {
	Email     string
	Username  string
	FullName  string
	OTP       string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type authService struct {
	userRepo    repository.UserRepository
	emailSender email.EmailSender
	jwtSecret   string
	otpStore    sync.Map // thread-safe in-memory OTP store matching Node.js
}

// NewAuthService creates a new authentication business service
func NewAuthService(userRepo repository.UserRepository, emailSender email.EmailSender, jwtSecret string) AuthService {
	return &authService{
		userRepo:    userRepo,
		emailSender: emailSender,
		jwtSecret:   jwtSecret,
	}
}

func (s *authService) CheckUsername(ctx context.Context, username string) (bool, string, error) {
	if len(username) < 3 {
		return false, "Username must be at least 3 characters", nil
	}

	user, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil && err != domain.ErrNotFound {
		return false, "Server error", err
	}

	if user != nil {
		return false, "Username is already taken", nil
	}

	return true, "Username is available", nil
}

func (s *authService) Register(ctx context.Context, username, password, fullName string) (*AuthResult, error) {
	if username == "" || password == "" || fullName == "" {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Name, username and password are required", 400)
	}
	if len(username) < 3 || len(username) > 30 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Username must be between 3 and 30 characters", 400)
	}
	if len(strings.TrimSpace(fullName)) < 2 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Please enter your full name", 400)
	}
	if len(password) < 6 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Password must be at least 6 characters", 400)
	}

	first, last := splitFullName(fullName)

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return nil, fmt.Errorf("password hash error: %w", err)
	}

	user := &domain.User{
		Username:          username,
		Password:          string(hashedPassword),
		FullName:          fullName,
		FirstName:         first,
		LastName:          last,
		Collections:       defaultCollections(),
		FavoritePeople:    []any{},
		Followers:         []string{},
		Following:         []string{},
		Admin:             false,
		ShowAdult:         false,
		CollectionVersion: 1,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if err == domain.ErrConflict {
			return nil, domain.NewAppError(domain.ErrConflict, "Username already exists", 409)
		}
		return nil, err
	}

	token, err := s.generateJWT(user.ID.Hex(), user.Username)
	if err != nil {
		return nil, err
	}

	res := &AuthResult{
		Message: "Account created successfully!",
		Token:   token,
	}
	res.User.ID = user.ID.Hex()
	res.User.Username = user.Username
	res.User.FullName = user.FullName
	res.User.Admin = false
	res.User.ShowAdult = false

	return res, nil
}

func (s *authService) Login(ctx context.Context, identifier, password string) (*AuthResult, error) {
	if identifier == "" || password == "" {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Email/Username and password are required", 400)
	}

	user, err := s.userRepo.FindByEmailOrUsername(ctx, identifier)
	if err != nil {
		if err == domain.ErrNotFound {
			return nil, domain.NewAppError(domain.ErrUnauthorized, "Invalid credentials", 401)
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, domain.NewAppError(domain.ErrUnauthorized, "Invalid credentials", 401)
	}

	token, err := s.generateJWT(user.ID.Hex(), user.Username)
	if err != nil {
		return nil, err
	}

	res := &AuthResult{
		Message: "Login successful!",
		Token:   token,
	}
	res.User.ID = user.ID.Hex()
	res.User.Username = user.Username
	res.User.Admin = user.Admin
	res.User.ShowAdult = user.ShowAdult

	return res, nil
}

func (s *authService) VerifyToken(ctx context.Context, userID string) (*TokenVerifyResult, error) {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		if err == domain.ErrNotFound {
			return nil, domain.NewAppError(domain.ErrNotFound, "User not found", 404)
		}
		return nil, err
	}

	return &TokenVerifyResult{
		Success:   true,
		Username:  user.Username,
		Admin:     user.Admin,
		ShowAdult: user.ShowAdult,
	}, nil
}

func (s *authService) GetMe(ctx context.Context, userID string) (*domain.User, error) {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		if err == domain.ErrNotFound {
			return nil, domain.NewAppError(domain.ErrNotFound, "User not found", 404)
		}
		return nil, err
	}
	return user, nil
}

func (s *authService) GetProfile(ctx context.Context, userID string) (*domain.User, error) {
	return s.GetMe(ctx, userID)
}

func (s *authService) UpdateProfile(ctx context.Context, userID string, update ProfileUpdate) (*domain.User, error) {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		if err == domain.ErrNotFound {
			return nil, domain.NewAppError(domain.ErrNotFound, "User not found", 404)
		}
		return nil, err
	}

	if update.FirstName != "" {
		user.FirstName = update.FirstName
	}
	if update.LastName != "" {
		user.LastName = update.LastName
	}
	if update.DateOfBirth != "" {
		user.DateOfBirth = update.DateOfBirth
	}
	if update.Bio != "" {
		user.Bio = update.Bio
	}
	if update.InstagramHandle != "" {
		user.InstagramHandle = update.InstagramHandle
	}
	if update.XHandle != "" {
		user.XHandle = update.XHandle
	}
	if update.YoutubeHandle != "" {
		user.YoutubeHandle = update.YoutubeHandle
	}
	if update.Avatar != "" {
		user.Avatar = update.Avatar
	}

	updateMap := map[string]any{
		"firstName":       user.FirstName,
		"lastName":        user.LastName,
		"dateOfBirth":     user.DateOfBirth,
		"bio":             user.Bio,
		"instagramHandle": user.InstagramHandle,
		"xHandle":         user.XHandle,
		"youtubeHandle":   user.YoutubeHandle,
		"avatar":          user.Avatar,
		"updatedAt":       time.Now(),
	}

	if err := s.userRepo.Update(ctx, userID, updateMap); err != nil {
		return nil, err
	}

	return user, nil
}

func (s *authService) SendOTP(ctx context.Context, emailAddress, username, fullName string) (string, error) {
	if emailAddress == "" || username == "" || fullName == "" {
		return "", domain.NewAppError(domain.ErrBadRequest, "Name, email and username are required", 400)
	}
	if !emailRegex.MatchString(emailAddress) {
		return "", domain.NewAppError(domain.ErrBadRequest, "Please enter a valid email address", 400)
	}
	if len(username) < 3 || len(username) > 30 {
		return "", domain.NewAppError(domain.ErrBadRequest, "Username must be between 3 and 30 characters", 400)
	}
	if len(strings.TrimSpace(fullName)) < 2 {
		return "", domain.NewAppError(domain.ErrBadRequest, "Please enter your full name", 400)
	}

	existing, _ := s.userRepo.FindByUsername(ctx, username)
	if existing != nil {
		return "", domain.NewAppError(domain.ErrConflict, "Username already exists", 409)
	}

	otp := generateRandomOTP()
	entry := otpEntry{
		Email:     emailAddress,
		Username:  username,
		FullName:  fullName,
		OTP:       otp,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	s.otpStore.Store(emailAddress, entry)

	html := fmt.Sprintf(`<html><body style="font-family:Arial,sans-serif;max-width:600px;margin:0 auto;padding:20px">
        <h2>Welcome to Soulstash, %s!</h2>
        <p>Your registration OTP:</p>
        <div style="background:#f4f4f4;padding:20px;text-align:center;margin:20px 0;border-radius:5px">
          <h1 style="font-size:32px;letter-spacing:5px;color:#007bff;margin:0">%s</h1>
        </div>
        <p>Expires in 10 minutes. Do not share.</p>
      </body></html>`, fullName, otp)

	if s.emailSender != nil {
		_ = s.emailSender.SendEmail(ctx, emailAddress, fullName, "Verify Your Email - Soulstash", html)
	}

	return otp, nil
}

func (s *authService) ForgotPassword(ctx context.Context, emailAddress string) error {
	if emailAddress == "" {
		return domain.NewAppError(domain.ErrBadRequest, "Email is required", 400)
	}

	user, err := s.userRepo.FindByEmailOrUsername(ctx, emailAddress)
	if err != nil || user == nil {
		return domain.NewAppError(domain.ErrNotFound, "Email not found in our records", 404)
	}

	otp := generateRandomOTP()
	expiresAt := time.Now().Add(15 * time.Minute)
	if err := s.userRepo.SavePasswordReset(ctx, emailAddress, otp, expiresAt); err != nil {
		return err
	}

	html := fmt.Sprintf(`<html><body style="font-family:Arial,sans-serif;max-width:600px;margin:0 auto;padding:20px">
        <h2>Password Reset Request</h2>
        <p>Hi %s, your reset OTP:</p>
        <div style="background:#f4f4f4;padding:20px;text-align:center;margin:20px 0;border-radius:5px">
          <h1 style="font-size:32px;letter-spacing:5px;color:#dc3545;margin:0">%s</h1>
        </div>
        <p>Expires in 15 minutes. Do not share.</p>
      </body></html>`, user.Username, otp)

	if s.emailSender != nil {
		_ = s.emailSender.SendEmail(ctx, emailAddress, user.Username, "Reset Your Password - Soulstash", html)
	}

	return nil
}

func (s *authService) ResetPassword(ctx context.Context, emailAddress, otp, newPassword string) error {
	if emailAddress == "" || otp == "" || newPassword == "" {
		return domain.NewAppError(domain.ErrBadRequest, "Email, OTP, and new password are required", 400)
	}
	if len(newPassword) < 6 {
		return domain.NewAppError(domain.ErrBadRequest, "Password must be at least 6 characters", 400)
	}

	resetID, err := s.userRepo.FindValidPasswordReset(ctx, emailAddress, otp)
	if err != nil {
		return domain.NewAppError(domain.ErrBadRequest, "Invalid or expired OTP", 400)
	}

	user, err := s.userRepo.FindByEmailOrUsername(ctx, emailAddress)
	if err != nil {
		return domain.NewAppError(domain.ErrBadRequest, "User not found", 400)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), 10)
	if err != nil {
		return err
	}

	if err := s.userRepo.UpdatePassword(ctx, user.ID.Hex(), string(hashed)); err != nil {
		return err
	}

	_ = s.userRepo.DeletePasswordReset(ctx, resetID)
	return nil
}

func (s *authService) VerifyOTPAndRegister(ctx context.Context, emailAddress, username, password, otp, fullName string) (*AuthResult, error) {
	if emailAddress == "" || username == "" || password == "" || otp == "" || fullName == "" {
		return nil, domain.NewAppError(domain.ErrBadRequest, "All fields are required", 400)
	}
	if len(password) < 6 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Password must be at least 6 characters", 400)
	}
	if len(strings.TrimSpace(fullName)) < 2 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Please enter your full name", 400)
	}

	val, ok := s.otpStore.Load(emailAddress)
	if !ok {
		return nil, domain.NewAppError(domain.ErrBadRequest, "OTP not found or expired", 400)
	}

	entry := val.(otpEntry)
	if time.Now().After(entry.ExpiresAt) {
		s.otpStore.Delete(emailAddress)
		return nil, domain.NewAppError(domain.ErrBadRequest, "OTP has expired", 400)
	}
	if entry.OTP != otp {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Invalid OTP", 400)
	}
	if entry.Email != emailAddress || entry.Username != username || entry.FullName != fullName {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Invalid request data", 400)
	}

	existing, _ := s.userRepo.FindByUsername(ctx, username)
	if existing != nil {
		s.otpStore.Delete(emailAddress)
		return nil, domain.NewAppError(domain.ErrConflict, "Username already exists", 409)
	}

	first, last := splitFullName(fullName)
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return nil, err
	}

	user := &domain.User{
		Username:          username,
		Email:             emailAddress,
		Password:          string(hashed),
		FullName:          fullName,
		FirstName:         first,
		LastName:          last,
		Collections:       defaultCollections(),
		Admin:             false,
		ShowAdult:         false,
		CollectionVersion: 1,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if err == domain.ErrConflict {
			s.otpStore.Delete(emailAddress)
			return nil, domain.NewAppError(domain.ErrConflict, "Username already exists", 409)
		}
		return nil, err
	}

	s.otpStore.Delete(emailAddress)

	token, err := s.generateJWT(user.ID.Hex(), user.Username)
	if err != nil {
		return nil, err
	}

	res := &AuthResult{
		Message: "Account created successfully!",
		Token:   token,
	}
	res.User.ID = user.ID.Hex()
	res.User.Username = user.Username
	res.User.Email = user.Email
	res.User.FullName = user.FullName
	res.User.Admin = false
	res.User.ShowAdult = false

	return res, nil
}

func (s *authService) generateJWT(userID, username string) (string, error) {
	claims := jwt.MapClaims{
		"userId":   userID,
		"username": username,
		"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtSecret))
}

func defaultCollections() []domain.UserCollection {
	now := time.Now()
	return []domain.UserCollection{
		{
			Name:        "Watched",
			IsDeletable: true,
			IsPublic:    false,
			IsPublished: false,
			Banner:      "https://cdn.imgchest.com/files/b23d0bfcaa8b.jpg",
			MovieCount:  0,
			Movies:      []any{},
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			Name:        "Watchlist",
			IsDeletable: true,
			IsPublic:    false,
			IsPublished: false,
			Banner:      "https://cdn.imgchest.com/files/b23d0bfcaa8b.jpg",
			MovieCount:  0,
			Movies:      []any{},
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

func splitFullName(fullName string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(fullName))
	if len(parts) == 0 {
		return "", ""
	}
	first := parts[0]
	last := ""
	if len(parts) > 1 {
		last = strings.Join(parts[1:], " ")
	}
	return first, last
}

func generateRandomOTP() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "123456"
	}
	return fmt.Sprintf("%06d", n.Int64()+100000)
}
