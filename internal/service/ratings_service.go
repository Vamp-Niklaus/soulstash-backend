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

	// 1. Check local DB cache
	cached, err := s.repo.FindRating(ctx, tmdbID, normType)
	if err == nil && cached != nil && cached.ImdbID != "" {
		if !isRatingExpired(cached) {
			cached.ImdbRating = filterImdbSentinel(cached.ImdbRating)
			cached.LookupAttempted = true
			cached.Source = "cache"
			return cached, nil
		}
		if imdbID == "" {
			imdbID = cached.ImdbID
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
	if imdbID != "" && len(s.omdbKeys) > 0 {
		_ = s.repo.IncrementMetric(ctx, "omdbFetchCount")
		rating, err := s.fetchOmdbRating(ctx, imdbID)
		if err == nil && rating != nil {
			imdbRating = rating
		} else if cached != nil && cached.ImdbRating != nil && *cached.ImdbRating < InvalidImdbSentinel {
			imdbRating = cached.ImdbRating
		}
	}

	// 4. Cache record into MongoDB
	ratingToStore := imdbRating
	if ratingToStore == nil {
		sentinel := InvalidImdbSentinel
		ratingToStore = &sentinel
	}

	record := &domain.RatingRecord{
		TmdbID:          tmdbID,
		MediaType:       normType,
		Type:            normType,
		ImdbID:          imdbID,
		ImdbRating:      ratingToStore,
		VoteAverage:     voteAvg,
		LookupAttempted: true,
		Source:          "fresh-lookup",
		UpdatedAt:       time.Now(),
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
	for i, itm := range items {
		go func(index int, item map[string]any) {
			cloned := make(map[string]any, len(item)+3)
			for k, v := range item {
				cloned[k] = v
			}

			tmdbID := int(getFloat(cloned["id"]))
			if tmdbID == 0 {
				tmdbID = int(getFloat(cloned["tmdbID"]))
			}
			if tmdbID == 0 {
				tmdbID = int(getFloat(cloned["tmdbId"]))
			}
			mediaType, _ := cloned["mediaType"].(string)
			if mediaType == "" {
				mediaType, _ = cloned["media_type"].(string)
			}
			if mediaType == "" {
				if _, ok := cloned["first_air_date"]; ok {
					mediaType = "Series"
				} else if _, ok := cloned["name"]; ok && cloned["title"] == nil {
					mediaType = "Series"
				}
			}
			imdbID, _ := cloned["imdb_id"].(string)
			var seedVoteAvg *float64
			if va := getFloat(cloned["vote_average"]); va > 0 {
				seedVoteAvg = &va
			}

			res, err := s.ResolveRating(ctx, tmdbID, mediaType, imdbID, seedVoteAvg)
			if err == nil && res != nil {
				cloned["imdb_rating"] = res.ImdbRating
				if res.ImdbID != "" {
					cloned["imdb_id"] = res.ImdbID
				}
				if res.VoteAverage != nil && (cloned["vote_average"] == nil || getFloat(cloned["vote_average"]) == 0) {
					cloned["vote_average"] = *res.VoteAverage
				}
				cloned["rating_lookup_attempted"] = true
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

	for attempt := 0; attempt < maxAttempts; attempt++ {
		apiKey := s.getNextOMDBKey()
		if apiKey == "" {
			return nil, nil
		}

		omdbURL := fmt.Sprintf("https://www.omdbapi.com/?i=%s&apikey=%s", url.QueryEscape(imdbID), url.QueryEscape(apiKey))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, omdbURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		var payload struct {
			Response   string `json:"Response"`
			Error      string `json:"Error"`
			ImdbRating string `json:"imdbRating"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()

		if decodeErr != nil {
			return nil, decodeErr
		}

		if payload.Response != "True" {
			errLower := strings.ToLower(payload.Error)
			// If key reached its daily limit or was invalid, try next key in the pool
			if strings.Contains(errLower, "limit") || strings.Contains(errLower, "key") {
				continue
			}
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

	return nil, nil
}

func filterImdbSentinel(val *float64) *float64 {
	if val == nil || *val <= 0 || *val == InvalidImdbSentinel {
		return nil
	}
	return val
}

func normalizeMediaType(m string) string {
	lower := strings.ToLower(strings.TrimSpace(m))
	if lower == "series" || lower == "tv" || lower == "show" {
		return "Series"
	}
	return "Movie"
}
