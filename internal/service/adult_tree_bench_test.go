package service

import (
	"testing"
)

func BenchmarkAdultTree_Evaluate(b *testing.B) {
	engine := NewAdultDecisionEngine()
	item := map[string]any{
		"adult":        false,
		"title":        "Inception",
		"overview":     "A thief who steals corporate secrets through dream-sharing technology is given the inverse task of planting an idea.",
		"vote_count":   35000,
		"vote_average": 8.4,
		"genre_ids":    []any{28, 878, 12},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = engine.Evaluate(item)
	}
}

func BenchmarkAdultTree_TestWithTrace(b *testing.B) {
	engine := NewAdultDecisionEngine()
	item := map[string]any{
		"adult":        false,
		"title":        "Erotic Romance",
		"overview":     "An exploration of love, passion, and intimacy.",
		"vote_count":   15,
		"vote_average": 4.2,
		"keywords": []any{
			map[string]any{"id": "207767", "name": "erotica"},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = engine.Test(nil, item)
	}
}
