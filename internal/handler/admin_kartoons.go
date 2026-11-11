package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"soulstash/internal/platform/kartoons"
)

// maskToken shows only the last 6 characters so you can recognise a token without exposing it.
func maskToken(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 6 {
		return "••••"
	}
	return "••••••••" + t[len(t)-6:]
}

type kartoonsTokensView struct {
	Configured       bool       `json:"configured"`
	AccessMasked     string     `json:"access_token_masked"`
	RefreshMasked    string     `json:"refresh_token_masked"`
	AccessExpiresAt  *time.Time `json:"access_expires_at,omitempty"`
	RefreshExpiresAt *time.Time `json:"refresh_expires_at,omitempty"`
	DaysLeft         float64    `json:"days_left"`
	LastRefreshedAt  *time.Time `json:"last_refreshed_at,omitempty"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	NeedsAction      bool       `json:"needs_action"`
	Message          string     `json:"message"`
}

func viewKartoonsTokens(s kartoons.Snapshot) kartoonsTokensView {
	v := kartoonsTokensView{
		Configured:       s.Configured,
		AccessMasked:     maskToken(s.AccessToken),
		RefreshMasked:    maskToken(s.RefreshToken),
		AccessExpiresAt:  s.AccessExpiresAt,
		RefreshExpiresAt: s.RefreshExpiresAt,
		DaysLeft:         s.DaysLeft,
		LastRefreshedAt:  s.LastRefreshedAt,
		LastCheckedAt:    s.LastCheckedAt,
		LastError:        s.LastError,
	}
	switch {
	case !s.Configured:
		v.NeedsAction, v.Message = true, "No tokens yet. Paste the access and refresh tokens below."
	case s.DaysLeft <= 0 && s.AccessExpiresAt != nil:
		v.NeedsAction, v.Message = true, "Access token has expired. Log in on kartoons.me and paste new tokens."
	case s.DaysLeft < 3 && s.AccessExpiresAt != nil:
		v.NeedsAction, v.Message = true, "Tokens expire in under 3 days. Log in on kartoons.me and paste new tokens."
	default:
		v.Message = "Tokens are valid."
	}
	return v
}

// GetKartoonsTokens returns the token status. Token values are never returned, only masks.
func (h *AdminHandler) GetKartoonsTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, viewKartoonsTokens(kartoons.Default.Status()))
}

// PutKartoonsTokens replaces the access and refresh tokens (RAM and MongoDB).
func (h *AdminHandler) PutKartoonsTokens(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	current := kartoons.Default.Status()
	access, refresh := body.AccessToken, body.RefreshToken
	if access == "" {
		access = current.AccessToken // empty field keeps the stored value
	}
	if refresh == "" {
		refresh = current.RefreshToken
	}
	if err := kartoons.Default.SetTokens(ctx, access, refresh); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save tokens: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, viewKartoonsTokens(kartoons.Default.Status()))
}

// RefreshKartoonsTokens tries a refresh now. It fails with a clear message when Kartoons
// asks for a browser verification.
func (h *AdminHandler) RefreshKartoonsTokens(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, viewKartoonsTokens(kartoons.Default.ForceRefresh(ctx)))
}
