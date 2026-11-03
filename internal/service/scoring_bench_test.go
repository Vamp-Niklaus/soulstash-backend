package service

import (
	"testing"
)

func BenchmarkComputeFinalScore(b *testing.B) {
	item := map[string]any{
		"title":             "The Dark Knight",
		"poster_path":       "/qJ2tW6WMUDux911r6m7haRef0WH.jpg",
		"release_date":      "2008-07-18",
		"release_year":      2008,
		"origin_country":    []any{"US", "GB"},
		"original_language": "en",
		"vote_count":        32000,
		"vote_average":      9.0,
	}
	query := "The Dark Knight"
	yearQuery := 2008

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ComputeFinalScore(item, query, yearQuery)
	}
}

func BenchmarkNormalizeMediaItem(b *testing.B) {
	rawMovie := map[string]any{
		"id":           155,
		"title":        "The Dark Knight",
		"release_date": "2008-07-18",
		"poster_path":  "/qJ2tW6WMUDux911r6m7haRef0WH.jpg",
		"vote_average": 9.0,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = NormalizeMediaItem(rawMovie, "movie")
	}
}
