// Package kartoons keeps a valid Kartoons bearer token for the backend.
//
// Kartoons login needs a Cloudflare Turnstile token, so a server cannot log in by
// itself. Log in once in a browser and put the access and refresh tokens in the
// environment (or in the kartoons_auth document in MongoDB). This package then:
//   - keeps both tokens and their expiry times in RAM and in MongoDB,
//   - loads them from MongoDB on startup,
//   - on every Maintain call (the /health cron ping) re-reads MongoDB, so a manual edit
//     there is picked up, and refreshes when the access token has under 3 days left,
//   - refreshes once more and retries when Kartoons answers 401.
package kartoons

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRefreshURL = "https://api.kartoons.to/api/auth/refresh"
	// Renew when the access token has less than this left.
	refreshMargin = 72 * time.Hour
	// Do not read MongoDB more often than this from Maintain.
	syncInterval = 30 * time.Second
)

// Record is what is stored in MongoDB.
type Record struct {
	Access       string
	Refresh      string
	ForceRefresh bool // set true in the document to make the backend refresh right away
}

// Store persists the token pair across restarts and lets you edit it by hand.
type Store interface {
	Load(ctx context.Context) (Record, error)
	Save(ctx context.Context, s Snapshot) error
	ClearForce(ctx context.Context) error
	// Ensure creates the document with empty token fields if it does not exist yet,
	// so the values show up in MongoDB and the admin page where you can fill them in.
	Ensure(ctx context.Context) error
}

// Snapshot is the state saved to MongoDB and shown on /health.
type Snapshot struct {
	Configured       bool       `json:"configured"`
	AccessToken      string     `json:"-"`
	RefreshToken     string     `json:"-"`
	AccessExpiresAt  *time.Time `json:"access_expires_at,omitempty"`
	RefreshExpiresAt *time.Time `json:"refresh_expires_at,omitempty"`
	DaysLeft         float64    `json:"days_left"`
	LastRefreshedAt  *time.Time `json:"last_refreshed_at,omitempty"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// Manager holds the current token pair and refreshes it.
type Manager struct {
	mu            sync.Mutex
	access        string
	refresh       string
	refreshURL    string
	http          *http.Client
	store         Store
	logger        *slog.Logger
	lastRefreshed time.Time
	lastChecked   time.Time
	lastError     string
	lastSync      time.Time
	busy          atomic.Bool
}

// Default is the shared manager, configured from the environment.
var Default = newFromEnv()

func newFromEnv() *Manager {
	access := strings.TrimSpace(os.Getenv("KARTOONS_ACCESS_TOKEN"))
	if access == "" {
		access = strings.TrimSpace(os.Getenv("KARTOONS_BEARER"))
	}
	refreshURL := strings.TrimSpace(os.Getenv("KARTOONS_REFRESH_URL"))
	if refreshURL == "" {
		refreshURL = defaultRefreshURL
	}
	return &Manager{
		access:     access,
		refresh:    strings.TrimSpace(os.Getenv("KARTOONS_REFRESH_TOKEN")),
		refreshURL: refreshURL,
		http:       &http.Client{Timeout: 10 * time.Second},
		logger:     slog.Default(),
	}
}

// UseStore connects MongoDB and loads the tokens at startup. Rules:
//   - MongoDB has tokens: they are used (the database is the source of truth).
//   - MongoDB has no tokens but the env vars do: the env values are copied into MongoDB.
//   - Neither: an empty kartoons_auth document is created so you know where to add them.
func (m *Manager) UseStore(ctx context.Context, store Store, logger *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
	if logger != nil {
		m.logger = logger
	}
	if err := store.Ensure(ctx); err != nil {
		m.logger.Warn("[Kartoons] could not create kartoons_auth document", slog.String("error", err.Error()))
	}
	rec, err := store.Load(ctx)
	if err != nil {
		m.logger.Warn("[Kartoons] could not read saved tokens", slog.String("error", err.Error()))
		return
	}
	switch {
	case rec.Access != "" || rec.Refresh != "":
		m.access, m.refresh = rec.Access, rec.Refresh
		m.logger.Info("[Kartoons] loaded tokens from MongoDB", slog.Time("access_expires", tokenExpiry(m.access)))
	case m.access != "" || m.refresh != "":
		m.logger.Info("[Kartoons] MongoDB was empty; copying tokens from environment")
		m.saveLocked()
	default:
		m.logger.Warn("[Kartoons] no tokens yet; add them in the admin page or in the kartoons_auth document in MongoDB")
	}
	m.lastSync = time.Now()
}

// SetTokens is used by the admin page. It replaces the pair in RAM and MongoDB.
// Empty values are allowed, which clears the stored token.
func (m *Manager) SetTokens(ctx context.Context, access, refresh string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.access = strings.TrimSpace(access)
	m.refresh = strings.TrimSpace(refresh)
	m.lastError = ""
	if m.store == nil {
		return nil
	}
	snap := m.snapshotLocked()
	return m.store.Save(ctx, snap)
}

// ForceRefresh is used by the admin page to renew the tokens now.
func (m *Manager) ForceRefresh(ctx context.Context) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked(ctx)
	return m.snapshotLocked()
}


// Enabled reports whether any token is configured.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.access != "" || m.refresh != ""
}

// Status returns the current state without secrets.
func (m *Manager) Status() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() Snapshot {
	s := Snapshot{
		Configured:   m.access != "" || m.refresh != "",
		AccessToken:  m.access,
		RefreshToken: m.refresh,
		LastError:    m.lastError,
	}
	if t := tokenExpiry(m.access); !t.IsZero() {
		s.AccessExpiresAt = &t
		s.DaysLeft = time.Until(t).Hours() / 24
	}
	if t := tokenExpiry(m.refresh); !t.IsZero() {
		s.RefreshExpiresAt = &t
	}
	if !m.lastRefreshed.IsZero() {
		t := m.lastRefreshed
		s.LastRefreshedAt = &t
	}
	if !m.lastChecked.IsZero() {
		t := m.lastChecked
		s.LastCheckedAt = &t
	}
	return s
}

// Maintain is meant for the /health cron ping. It never blocks the caller for long:
// start it in a goroutine. It re-reads MongoDB (a manual edit there wins when its tokens
// last longer, or when force_refresh is true) and refreshes when 3 days or less remain.
func (m *Manager) Maintain(ctx context.Context) {
	if !m.busy.CompareAndSwap(false, true) {
		return
	}
	defer m.busy.Store(false)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastChecked = time.Now()

	force := false
	if m.store != nil && time.Since(m.lastSync) >= syncInterval {
		m.lastSync = time.Now()
		if rec, err := m.store.Load(context.Background()); err == nil {
			if rec.Refresh != "" && (rec.Access != m.access || rec.Refresh != m.refresh) {
				m.access, m.refresh = rec.Access, rec.Refresh
				m.lastError = ""
				m.logger.Info("[Kartoons] picked up tokens edited in MongoDB")
			}
			force = rec.ForceRefresh
		}
	}

	if force || m.access == "" || time.Until(tokenExpiry(m.access)) < refreshMargin {
		m.refreshLocked(ctx)
		if force && m.store != nil {
			clearCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = m.store.ClearForce(clearCtx)
			cancel()
		}
	}
}

// Do sends req with the bearer token for Kartoons hosts. It refreshes the token when it
// is close to expiry and retries once after a 401. Only use it for requests without a body.
func (m *Manager) Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if !isKartoonsHost(req.URL.Hostname()) || !m.Enabled() {
		return client.Do(req)
	}
	ctx := req.Context()

	m.mu.Lock()
	if m.access == "" || time.Until(tokenExpiry(m.access)) < refreshMargin {
		m.refreshLocked(ctx)
	}
	used := m.access
	m.mu.Unlock()

	resp, err := m.send(client, req, used)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()

	m.mu.Lock()
	// Another request may already have refreshed while we waited for the lock.
	if m.access == used {
		m.refreshLocked(ctx)
	}
	retry := m.access
	m.mu.Unlock()
	return m.send(client, req, retry)
}

func (m *Manager) send(client *http.Client, req *http.Request, token string) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if token != "" {
		clone.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(clone)
}

// refreshLocked renews the token pair. The caller must hold m.mu.
func (m *Manager) refreshLocked(ctx context.Context) {
	if m.refresh == "" {
		m.lastError = "no refresh token configured"
		m.logger.Warn("[Kartoons] no refresh token configured; set KARTOONS_REFRESH_TOKEN or edit kartoons_auth in MongoDB")
		return
	}
	if exp := tokenExpiry(m.refresh); !exp.IsZero() && time.Until(exp) <= 0 {
		m.lastError = "refresh token expired; log in again and update kartoons_auth"
		m.logger.Error("[Kartoons] refresh token expired; log in again and update kartoons_auth")
		return
	}
	access, refresh, err := m.callRefresh(ctx)
	if err != nil {
		m.lastError = err.Error()
		m.logger.Warn("[Kartoons] token refresh failed", slog.String("error", err.Error()))
		return
	}
	m.access = access
	if refresh != "" {
		m.refresh = refresh
	}
	m.lastRefreshed = time.Now()
	m.lastError = ""
	m.logger.Info("[Kartoons] token refreshed", slog.Time("access_expires", tokenExpiry(m.access)))
	m.saveLocked()
}

func (m *Manager) saveLocked() {
	if m.store == nil {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.Save(saveCtx, m.snapshotLocked()); err != nil {
		m.logger.Warn("[Kartoons] could not save tokens", slog.String("error", err.Error()))
	}
}

func (m *Manager) callRefresh(ctx context.Context) (string, string, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": m.refresh})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.refreshURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.refresh)

	resp, err := m.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("refresh endpoint returned %d", resp.StatusCode)
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Data         struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	access, refresh := out.Data.AccessToken, out.Data.RefreshToken
	if access == "" {
		access, refresh = out.AccessToken, out.RefreshToken
	}
	if access == "" {
		return "", "", errors.New("refresh response had no access_token")
	}
	return access, refresh, nil
}

func isKartoonsHost(host string) bool {
	host = strings.ToLower(host)
	for _, d := range []string{"kartoons.me", "kartoons.to"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// tokenExpiry reads the exp claim of a JWT without verifying it. Zero time if unreadable.
func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}
