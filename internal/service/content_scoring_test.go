package service

import (
	"testing"
)

func TestContentScoring_ComputeFinalScore(t *testing.T) {
	tests := []struct {
		name      string
		item      map[string]any
		query     string
		yearQuery int
		expectMin float64
		expectMax float64
	}{
		{
			name: "Exact title match movie with Indian origin and poster",
			item: map[string]any{
				"title":             "RRR",
				"poster_path":       "/rrr_poster.jpg",
				"release_date":      "2022-03-24",
				"release_year":      2022,
				"origin_country":    []any{"IN"},
				"original_language": "te",
				"vote_count":        5000,
				"vote_average":      8.0,
			},
			query:     "RRR",
			yearQuery: 2022,
			expectMin: 200.0,
			expectMax: 400.0,
		},
		{
			name: "Foreign title with missing poster penalization",
			item: map[string]any{
				"title":             "Random Indie Movie",
				"poster_path":       "",
				"release_date":      "2010-01-01",
				"original_language": "fr",
				"vote_count":        10,
				"vote_average":      5.0,
			},
			query:     "Random",
			yearQuery: 0,
			expectMin: -50.0,
			expectMax: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeFinalScore(tt.item, tt.query, tt.yearQuery)
			if score < tt.expectMin || score > tt.expectMax {
				t.Errorf("expected score between %f and %f, got %f", tt.expectMin, tt.expectMax, score)
			}
		})
	}
}

func TestContentScoring_NormalizeMediaItem(t *testing.T) {
	rawMovie := map[string]any{
		"id":           123,
		"title":        "Inception",
		"release_date": "2010-07-16",
		"poster_path":  "/poster.jpg",
		"vote_average": 8.8,
	}

	normalized := NormalizeMediaItem(rawMovie, "movie")
	if normalized["media_type"] != "Movie" {
		t.Errorf("expected media_type 'Movie', got %v", normalized["media_type"])
	}
	if yr, ok := normalized["release_year"].(*int); !ok || *yr != 2010 {
		t.Errorf("expected release_year 2010, got %v", normalized["release_year"])
	}

	rawTV := map[string]any{
		"id":             456,
		"name":           "Breaking Bad",
		"first_air_date": "2008-01-20",
		"poster_path":    "/bb.jpg",
		"vote_average":   9.5,
	}

	normalizedTV := NormalizeMediaItem(rawTV, "tv")
	if normalizedTV["media_type"] != "Series" {
		t.Errorf("expected media_type 'Series', got %v", normalizedTV["media_type"])
	}
	if normalizedTV["title"] != "Breaking Bad" {
		t.Errorf("expected title 'Breaking Bad', got %v", normalizedTV["title"])
	}
}
