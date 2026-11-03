package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/tmdb"
	"soulstash/internal/repository"
)

const InvalidImdbSentinel = 10.0

type RatingsService interface {
	ResolveRating(ctx context.Context, tmdbID int, mediaType string, seedImdbID string, seedVoteAvg *float64) (*domain.RatingRecord, error)
	BatchEnrich(ctx context.Context, items []map[string]any) []map[string]any
	GetCounters(ctx context.Context) (*domain.AppMetrics, error)
}

type ratingsService struct {
	repo       repository.RatingsRepository
	tmdbClient tmdb.Client
	omdbAPIKey string
	httpClient *http.Client
}

func NewRatingsService(repo repository.RatingsRepository, tmdbClient tmdb.Client, omdbAPIKey string) RatingsService {
	return &ratingsService{
		repo:       repo,
		tmdbClient: tmdbClient,
		omdbAPIKey: omdbAPIKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
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

	// 1. Check local DB cache
	cached, err := s.repo.FindRating(ctx, tmdbID, normType)
	if err == nil && cached != nil && cached.ImdbID != "" {
		cached.ImdbRating = filterImdbSentinel(cached.ImdbRating)
		cached.LookupAttempted = true
		cached.Source = "cache"
		return cached, nil
	}

	// 2. Resolve IMDb ID and TMDB vote average
	imdbID := strings.TrimSpace(seedImdbID)
	voteAvg := seedVoteAvg

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
	if imdbID != "" && s.omdbAPIKey != "" {
		_ = s.repo.IncrementMetric(ctx, "omdbFetchCount")
		rating, err := s.fetchOmdbRating(ctx, imdbID)
		if err == nil && rating != nil {
			imdbRating = rating
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
	type result struct {
		idx  int
		item map[string]any
	}

	ch := make(chan result, len(items))
	for i, itm := range items {
		go func(index int, item map[string]any) {
			tmdbID := int(getFloat(item["id"]))
			if tmdbID == 0 {
				tmdbID = int(getFloat(item["tmdbID"]))
			}
			mediaType, _ := item["mediaType"].(string)
			if mediaType == "" {
				mediaType, _ = item["media_type"].(string)
			}
			imdbID, _ := item["imdb_id"].(string)

			res, err := s.ResolveRating(ctx, tmdbID, mediaType, imdbID, nil)
			if err == nil && res != nil {
				item["imdb_rating"] = res.ImdbRating
				item["imdb_id"] = res.ImdbID
				item["rating_lookup_attempted"] = true
			}
			ch <- result{idx: index, item: item}
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

func (s *ratingsService) fetchOmdbRating(ctx context.Context, imdbID string) (*float64, error) {
	omdbURL := fmt.Sprintf("https://www.omdbapi.com/?i=%s&apikey=%s", url.QueryEscape(imdbID), url.QueryEscape(s.omdbAPIKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, omdbURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		Response   string `json:"Response"`
		ImdbRating string `json:"imdbRating"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	if payload.Response != "True" || payload.ImdbRating == "N/A" || payload.ImdbRating == "" {
		return nil, nil
	}

	val, err := strconv.ParseFloat(payload.ImdbRating, 64)
	if err != nil || val <= 0 || val == InvalidImdbSentinel {
		return nil, nil
	}

	return &val, nil
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
