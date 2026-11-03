package service_test

import (
	"context"
	"strings"
	"testing"

	"soulstash/internal/service"
)

func TestAuthService_CheckUsername(t *testing.T) {
	repo := newMockUserRepo()
	jwtSecret := "supersecretjwtkeythatislongenough123"
	authSvc := service.NewAuthService(repo, nil, jwtSecret)

	// Short username
	avail, msg, err := authSvc.CheckUsername(context.Background(), "ab")
	if err != nil || avail || !strings.Contains(msg, "at least 3") {
		t.Errorf("expected short username rejection, got avail=%v msg=%s", avail, msg)
	}

	// Available username
	avail, msg, err = authSvc.CheckUsername(context.Background(), "cooluser")
	if err != nil || !avail {
		t.Errorf("expected available username, got avail=%v msg=%s err=%v", avail, msg, err)
	}
}

func TestAuthService_RegisterAndLogin(t *testing.T) {
	repo := newMockUserRepo()
	jwtSecret := "supersecretjwtkeythatislongenough123"
	authSvc := service.NewAuthService(repo, nil, jwtSecret)

	// Register
	res, err := authSvc.Register(context.Background(), "johndoe", "secret123", "John Doe")
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}
	if res.User.Username != "johndoe" || res.Token == "" {
		t.Errorf("expected registered user with token, got %v", res)
	}

	// Login with correct password
	loginRes, err := authSvc.Login(context.Background(), "johndoe", "secret123")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if loginRes.Token == "" {
		t.Errorf("expected non-empty token on login")
	}

	// Login with invalid password
	_, err = authSvc.Login(context.Background(), "johndoe", "wrongpassword")
	if err == nil {
		t.Errorf("expected error on wrong password, got nil")
	}

	// Verify Token with User ID
	verifyRes, err := authSvc.VerifyToken(context.Background(), loginRes.User.ID)
	if err != nil || verifyRes.Username != "johndoe" {
		t.Errorf("expected valid verification for johndoe, got result=%v err=%v", verifyRes, err)
	}

	// Get Profile
	profile, err := authSvc.GetProfile(context.Background(), "johndoe")
	if err != nil || profile.Username != "johndoe" {
		t.Errorf("expected profile for johndoe, got %v", profile)
	}
}
