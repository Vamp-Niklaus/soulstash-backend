package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/repository"
)

const (
	InvalidImdbSentinel = 10.0
	ValidRatingTTL      = 30 * 24 * time.Hour // 30 days
	SentinelRatingTTL   = 14 * 24 * time.Hour // 14 days
)

func isRatingExpired(record *domain.RatingRecord) bool {
	if record == nil || record.UpdatedAt.IsZero() {
		return true
	}
	age := time.Since(record.UpdatedAt)
	if record.ImdbRating == nil || *record.ImdbRating >= InvalidImdbSentinel {
		return age > SentinelRatingTTL
	}
	return age > ValidRatingTTL
}

type RatingsService interface {
	ResolveRating(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error)
	GetStoredRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error)
	ListRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error)
	BatchEnrich(ctx context.Context, items []map[string]any) []map[string]any
	GetCounters(ctx context.Context) (*domain.AppMetrics, error)
}

type ratingsService struct {
	repo       repository.RatingsRepository
	tmdbClient tmdb.Client
	omdbKeys   []string
	keyIndex   uint64
	httpClient *http.Client
}

func NewRatingsService(repo repository.RatingsRepository, tmdbClient tmdb.Client, omdbKeys ...string) RatingsService {
	var cleanKeys []string
	seen := make(map[string]bool)
	for _, k := range omdbKeys {
		for _, part := range strings.Split(k, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" && !seen[trimmed] {
				seen[trimmed] = true
				cleanKeys = append(cleanKeys, trimmed)
			}
		}
	}

	return &ratingsService{
		repo:       repo,
		tmdbClient: tmdbClient,
		omdbKeys:   cleanKeys,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *ratingsService) getNextOMDBKey() string {
	if len(s.omdbKeys) == 0 {
		return ""
	}
	if len(s.omdbKeys) == 1 {
		return s.omdbKeys[0]
	}
	idx := atomic.AddUint64(&s.keyIndex, 1) - 1
	return s.omdbKeys[idx%uint64(len(s.omdbKeys))]
}

func (s *ratingsService) ResolveRating(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error) {
	normType := normalizeMediaType(mediaType)
	if tmdbID <= 0 {
		return &domain.RatingRecord{
			TmdbID:          tmdbID,
			MediaType:       normType,
			Type:            normType,
			ImdbID:          "",
			ImdbRating:      nil,
			VoteAverage:     nil,
			LookupAttempted: false,
			Source:          "invalid-tmdb-id",
		}, nil
	}

	imdbID := strings.TrimSpace(seedImdbID)
	voteAvg := seedVoteAvg

	// 1. Check local DB cache (honors valid 30d TTL and 10.0 sentinel 14d TTL even when imdbID is empty)
	cached, err := s.repo.FindRating(ctx, tmdbID, normType)
	if err == nil && cached != nil {
		if !isRatingExpired(cached) {
			res := *cached
			res.ImdbRating = filterImdbSentinel(cached.ImdbRating)
			res.LookupAttempted = true
			res.Source = "cache"
			return &res, nil
		}
		if imdbID == "" {
			imdbID = strings.TrimSpace(cached.ImdbID)
		}
		if voteAvg == nil {
			voteAvg = cached.VoteAverage
		}
	}

	// 2. Resolve IMDb ID and TMDB vote average
	if imdbID == "" && s.tmdbClient != nil {
		_ = s.repo.IncrementMetric(ctx, "tmdbDetailFetchCount")
		endpoint := fmt.Sprintf("/3/movie/%d", tmdbID)
		if normType == "Series" {
			endpoint = fmt.Sprintf("/3/tv/%d", tmdbID)
		}

		data, err := s.tmdbClient.Get(ctx, endpoint, map[string]string{"language": "en-US"})
		if err == nil && data != nil {
			if idStr, ok := data["imdb_id"].(string); ok {
				imdbID = strings.TrimSpace(idStr)
			}
			if voteAvg == nil {
				if va := getFloat(data["vote_average"]); va > 0 {
					voteAvg = &va
				}
			}
		}

		if normType == "Series" && imdbID == "" {
			extData, err := s.tmdbClient.Get(ctx, fmt.Sprintf("/3/tv/%d/external_ids", tmdbID), nil)
			if err == nil && extData != nil {
				if idStr, ok := extData["imdb_id"].(string); ok {
					imdbID = strings.TrimSpace(idStr)
				}
			}
		}
	}

	// 3. Query OMDb if IMDb ID found
	var imdbRating *float64
	var omdbTransientErr error
	if imdbID != "" && len(s.omdbKeys) > 0 {
		_ = s.repo.IncrementMetric(ctx, "omdbFetchCount")
		rating, err := s.fetchOmdbRating(ctx, imdbID)
		if err != nil {
			omdbTransientErr = err
		}
		if err == nil && rating != nil {
			imdbRating = rating
		} else if err != nil && cached != nil && cached.ImdbRating != nil && *cached.ImdbRating < InvalidImdbSentinel {
			imdbRating = cached.ImdbRating
		}
	}

	// Avoid overwriting/poisoning cache with 10.0 sentinel when OMDb failed due to quota or transient network error
	if omdbTransientErr != nil {
		var ts time.Time
		if cached != nil {
			ts = cached.UpdatedAt
		}
		return &domain.RatingRecord{
			TmdbID:          tmdbID,
			MediaType:       normType,
			Type:            normType,
			ImdbID:          imdbID,
			ImdbRating:      filterImdbSentinel(imdbRating),
			VoteAverage:     voteAvg,
			LookupAttempted: false,
			Source:          "omdb-unavailable",
			UpdatedAt:       ts,
		}, nil
	}

	// 4. Upsert rating (either resolved rating or 10.0 sentinel) with fresh UpdatedAt
	ratingToStore := imdbRating
	if ratingToStore == nil {
		sentinel := InvalidImdbSentinel
		ratingToStore = &sentinel
	}

	now := time.Now()
	record := &domain.RatingRecord{
		TmdbID:          tmdbID,
		MediaType:       normType,
		Type:            normType,
		ImdbID:          imdbID,
		ImdbRating:      ratingToStore,
		VoteAverage:     voteAvg,
		LookupAttempted: true,
		Source:          "fresh-lookup",
		UpdatedAt:       now,
	}

	_ = s.repo.UpsertRating(ctx, record)

	// Return client-safe response (filtering 10.0 sentinel to null)
	clientRecord := *record
	clientRecord.ImdbRating = filterImdbSentinel(ratingToStore)
	return &clientRecord, nil
}

func (s *ratingsService) BatchEnrich(ctx context.Context, items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return items
	}
	type result struct {
		idx  int
		item map[string]any
	}

	ch := make(chan result, len(items))
	sem := make(chan struct{}, 10)
	for i, itm := range items {
		go func(index int, item map[string]any) {
			sem <- struct{}{}
			defer func() { <-sem }()

			cloned := make(map[string]any, len(item)+8)
			for k, v := range item {
				cloned[k] = v
			}

			tmdbID := extractIntField(cloned, "id", "tmdbID", "tmdbId", "contentId", "movieId", "seriesId", "mediaId")
			normType := resolveMediaTypeFromMap(cloned)
			imdbID, _ := cloned["imdb_id"].(string)
			if imdbID == "" {
				imdbID, _ = cloned["imdbID"].(string)
			}
			var seedVoteAvg *float64
			if va := getFloat(cloned["vote_average"]); va > 0 && va < InvalidImdbSentinel {
				seedVoteAvg = &va
			}

			if tmdbID > 0 {
				cloned["tmdbID"] = tmdbID
			}
			cloned["mediaType"] = normType

			res, err := s.ResolveRating(ctx, tmdbID, normType, imdbID, seedVoteAvg)
			if err == nil && res != nil {
				cloned["imdb_rating"] = res.ImdbRating
				if res.ImdbID != "" {
					cloned["imdb_id"] = res.ImdbID
					cloned["imdbID"] = res.ImdbID
				}
				if res.VoteAverage != nil && *res.VoteAverage > 0 && *res.VoteAverage < InvalidImdbSentinel &&
					(cloned["vote_average"] == nil || getFloat(cloned["vote_average"]) == 0 || getFloat(cloned["vote_average"]) >= InvalidImdbSentinel) {
					cloned["vote_average"] = *res.VoteAverage
				}
				cloned["lookup_attempted"] = res.LookupAttempted
				cloned["rating_lookup_attempted"] = res.LookupAttempted
				if !res.UpdatedAt.IsZero() {
					cloned["rating_checked_at"] = res.UpdatedAt.UTC().Format(time.RFC3339)
				}
			}
			ch <- result{idx: index, item: cloned}
		}(i, itm)
	}

	enriched := make([]map[string]any, len(items))
	for i := 0; i < len(items); i++ {
		r := <-ch
		enriched[r.idx] = r.item
	}
	return enriched
}

func (s *ratingsService) GetCounters(ctx context.Context) (*domain.AppMetrics, error) {
	return s.repo.GetMetrics(ctx)
}

func (s *ratingsService) ListRatings(ctx context.Context, mediaType string, limit int) ([]domain.RatingRecord, error) {
	normType := ""
	if strings.TrimSpace(mediaType) != "" {
		normType = normalizeMediaType(mediaType)
	}
	return s.repo.FindRatings(ctx, normType, limit)
}

func (s *ratingsService) GetStoredRating(ctx context.Context, tmdbID int, mediaType string) (*domain.RatingRecord, error) {
	if tmdbID <= 0 {
		return nil, domain.NewAppError(domain.ErrBadRequest, "Valid tmdbID is required", 400)
	}
	record, err := s.repo.FindRating(ctx, tmdbID, normalizeMediaType(mediaType))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || record == nil {
			return nil, domain.NewAppError(domain.ErrNotFound, "Rating row not found", 404)
		}
		return nil, err
	}
	if record == nil {
		return nil, domain.NewAppError(domain.ErrNotFound, "Rating row not found", 404)
	}

	if isRatingExpired(record) {
		fresh, err := s.ResolveRating(ctx, tmdbID, mediaType, record.ImdbID, record.VoteAverage)
		if err == nil && fresh != nil {
			return fresh, nil
		}
	}

	record.ImdbRating = filterImdbSentinel(record.ImdbRating)
	return record, nil
}

func (s *ratingsService) fetchOmdbRating(ctx context.Context, imdbID string) (*float64, error) {
	if len(s.omdbKeys) == 0 {
		return nil, nil
	}

	maxAttempts := len(s.omdbKeys)
	if maxAttempts > 3 {
		maxAttempts = 3
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		apiKey := s.getNextOMDBKey()
		if apiKey == "" {
			return nil, lastErr
		}

		omdbURL := fmt.Sprintf("https://www.omdbapi.com/?i=%s&apikey=%s", url.QueryEscape(imdbID), url.QueryEscape(apiKey))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, omdbURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := s.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("omdb http %d", resp.StatusCode)
			continue
		}

		var payload struct {
			Response   string `json:"Response"`
			Error      string `json:"Error"`
			ImdbRating string `json:"imdbRating"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()

		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}

		if payload.Response != "True" {
			errLower := strings.ToLower(payload.Error)
			// If key reached its daily limit or was invalid, try next key in the pool
			if strings.Contains(errLower, "limit") || strings.Contains(errLower, "key") {
				lastErr = errors.New(payload.Error)
				continue
			}
			// Genuine not found / no data response from OMDb
			return nil, nil
		}

		if payload.ImdbRating == "N/A" || payload.ImdbRating == "" {
			return nil, nil
		}

		val, err := strconv.ParseFloat(payload.ImdbRating, 64)
		if err != nil || val <= 0 || val == InvalidImdbSentinel {
			return nil, nil
		}

		return &val, nil
	}

	return nil, lastErr
}

func extractIntField(m map[string]any, keys ...string) int {
	for _, k := range keys {
		val, exists := m[k]
		if !exists || val == nil {
			continue
		}
		if f := getFloat(val); f > 0 {
			return int(f)
		}
		if s, ok := val.(string); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func filterImdbSentinel(val *float64) *float64 {
	if val == nil || *val <= 0 || *val == InvalidImdbSentinel {
		return nil
	}
	return val
}

func resolveMediaTypeFromMap(m map[string]any) string {
	mediaType, _ := m["mediaType"].(string)
	if mediaType == "" {
		mediaType, _ = m["media_type"].(string)
	}
	if mediaType == "" {
		mediaType, _ = m["type"].(string)
	}
	if mediaType == "" {
		if extractIntField(m, "seriesId", "series_id") > 0 {
			mediaType = "Series"
		} else if extractIntField(m, "movieId", "movie_id") > 0 {
			mediaType = "Movie"
		} else if fa, _ := m["first_air_date"].(string); strings.TrimSpace(fa) != "" {
			mediaType = "Series"
		} else if _, ok := m["first_air_date"]; ok {
			mediaType = "Series"
		} else if _, ok := m["name"]; ok && m["title"] == nil {
			mediaType = "Series"
		}
	}
	return normalizeMediaType(mediaType)
}

func normalizeMediaType(m string) string {
	lower := strings.ToLower(strings.TrimSpace(m))
	if lower == "series" || lower == "tv" || lower == "show" {
		return "Series"
	}
	return "Movie"
}
