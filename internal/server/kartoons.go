package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"soulstash/internal/platform/kartoons"

	"github.com/go-chi/chi/v5"
)

// kartoonsToken is the Stremio addon token used by the Kartoons source.
// Prefer setting KARTOONS_TOKEN in the environment; the fallback matches the
// value from the retired Node backend so behavior is unchanged until it is set.
const kartoonsTokenFallback = "OK_DGA031TQOAsyYaGa3LrOu9xdIjNCDRsxbos0-XCs"

var (
	kartoonsHTTP        = &http.Client{Timeout: 10 * time.Second}
	kartoonsLabelSuffix = regexp.MustCompile(`(?i)\s*-\s*S\d+E\d+\s*$`)
)

type kartoonsStream struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// handleKartoonsResolveStream ports GET /api/kartoons/resolve-stream from the
// retired Node backend. It resolves a Kartoons id (or slug) to playable stream URLs.
func (s *Server) handleKartoonsResolveStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	if typ == "" {
		typ = "movie"
	}
	targetID := q.Get("id")
	slug := q.Get("slug")
	ctx := r.Context()

	writeJSON := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	if targetID == "" && slug != "" {
		infoURL := "https://api.kartoons.to/api/movies/" + url.PathEscape(slug)
		if typ == "series" {
			infoURL = "https://api.kartoons.to/api/shows/" + url.PathEscape(slug)
		}
		body, status, err := kartoonsFetch(ctx, infoURL, nil, 5*time.Second)
		if err == nil && status == http.StatusOK {
			var info struct {
				Data struct {
					ID string `json:"_id"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &info) == nil && info.Data.ID != "" {
				targetID = info.Data.ID
			}
		}
	}

	if targetID == "" {
		writeJSON(http.StatusBadRequest, map[string]any{
			"success": false,
			"error":   "Missing id or slug",
			"streams": []kartoonsStream{},
		})
		return
	}

	token := kartoonsStremioToken()
	stremioURL := "https://api.kartoons.me/api/stremio/stream/" +
		url.PathEscape(typ) + "/" + url.PathEscape(targetID) + ".json?token=" + url.QueryEscape(token)

	body, status, err := kartoonsFetch(ctx, stremioURL, nil, 6*time.Second)
	if err != nil {
		s.logger.Warn("[Kartoons Stream Resolver] error", slog.String("error", err.Error()))
		writeJSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   err.Error(),
			"streams": []kartoonsStream{},
		})
		return
	}
	if status != http.StatusOK {
		writeJSON(status, map[string]any{"success": false, "streams": []kartoonsStream{}})
		return
	}

	var stremio struct {
		Streams []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			Name  string `json:"name"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(body, &stremio)

	playable := make([]kartoonsStream, 0, len(stremio.Streams))
	hopHeaders := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"Origin":     "https://web.stremio.com",
		"Referer":    "https://web.stremio.com/",
	}

	for _, st := range stremio.Streams {
		if st.URL == "" {
			continue
		}
		playableURL := st.URL

		// Follow the worker hop: the URL may return a text body whose first
		// http line is the real playable URL.
		if hopBody, hopStatus, hopErr := kartoonsFetch(ctx, st.URL, hopHeaders, 5*time.Second); hopErr == nil && hopStatus == http.StatusOK {
			for _, line := range strings.Split(string(hopBody), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "http") {
					playableURL = line
					break
				}
			}
		}

		label := st.Title
		if label == "" {
			label = st.Name
		}
		if label == "" {
			label = "Kartoon Stream"
		}
		label = strings.TrimSpace(kartoonsLabelSuffix.ReplaceAllString(label, ""))

		playable = append(playable, kartoonsStream{URL: playableURL, Label: label})
	}

	if len(playable) == 0 {
		s.logger.Warn("[Kartoons Stream Resolver] no playable streams",
			slog.Int("upstreamStatus", status), slog.Int("bodyLen", len(body)))
		writeJSON(http.StatusBadGateway, map[string]any{"success": false, "streams": []kartoonsStream{}})
		return
	}

	writeJSON(http.StatusOK, map[string]any{"success": true, "streams": playable})
}

// kartoonsStremioToken is the Stremio addon token (env KARTOONS_TOKEN, else the fallback).
func kartoonsStremioToken() string {
	if token := os.Getenv("KARTOONS_TOKEN"); token != "" {
		return token
	}
	return kartoonsTokenFallback
}

// handleKartoonsProxy serves GET /api/kartoons/proxy/*. The frontend calls this instead of
// api.kartoons.me, so the backend adds the bearer (and the Stremio token) and the browser
// never needs one. Only the catalog paths the app uses are allowed.
func (s *Server) handleKartoonsProxy(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
	}

	rest := strings.Trim(chi.URLParam(r, "*"), "/")
	if rest == "" || strings.Contains(rest, "..") || strings.ContainsAny(rest, "\\?#") {
		fail(http.StatusBadRequest, "Invalid path")
		return
	}
	first := strings.SplitN(rest, "/", 2)[0]
	if first != "shows" && first != "movies" && first != "stremio" {
		fail(http.StatusForbidden, "Path not allowed")
		return
	}

	q := r.URL.Query()
	q.Del("token")
	if first == "stremio" {
		q.Set("token", kartoonsStremioToken())
	}

	var (
		body   []byte
		status int
		err    error
	)
	for _, host := range []string{"api.kartoons.me", "api.kartoons.to"} {
		u := url.URL{Scheme: "https", Host: host, Path: "/api/" + rest, RawQuery: q.Encode()}
		body, status, err = kartoonsFetch(r.Context(), u.String(), map[string]string{"Accept": "application/json"}, 10*time.Second)
		if err == nil && status != http.StatusNotFound && status < 500 {
			break
		}
	}
	if err != nil {
		s.logger.Warn("[Kartoons Proxy] upstream error", slog.String("path", rest), slog.String("error", err.Error()))
		fail(http.StatusBadGateway, "Kartoons is unreachable")
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		s.logger.Warn("[Kartoons Proxy] upstream rejected the token", slog.Int("status", status))
		fail(http.StatusBadGateway, "Kartoons authentication failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusOK {
		w.Header().Set("Cache-Control", "public, max-age=60")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// kartoonsFetch performs a GET with a timeout and returns the body (capped at 2 MB).
func kartoonsFetch(parent context.Context, rawURL string, headers map[string]string, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := kartoons.Default.Do(kartoonsHTTP, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, err
}
