package service

import (
	"testing"
)

func TestAdultDecisionTree_Evaluate(t *testing.T) {
	engine := NewAdultDecisionEngine()

	tests := []struct {
		name          string
		item          map[string]any
		expectedAdult bool
	}{
		{
			name: "Explicit adult flag true",
			item: map[string]any{
				"adult": true,
				"title": "Some Explicit Movie",
			},
			expectedAdult: true,
		},
		{
			name: "Mainstream popular movie with high vote count",
			item: map[string]any{
				"adult":        false,
				"title":        "The Avengers",
				"vote_count":   25000,
				"vote_average": 8.1,
				"genre_ids":    []any{28, 12, 878},
			},
			expectedAdult: false,
		},
		{
			name: "Movie with softcore porn keyword",
			item: map[string]any{
				"adult":        false,
				"title":        "Erotic Thriller",
				"vote_count":   12,
				"vote_average": 4.5,
				"keywords": []any{
					map[string]any{"id": "155477", "name": "softcore porn"},
				},
			},
			expectedAdult: true,
		},
		{
			name: "Title containing adult trigger word",
			item: map[string]any{
				"adult":        false,
				"title":        "Sensual Pleasures",
				"vote_count":   5,
				"vote_average": 3.0,
			},
			expectedAdult: true,
		},
		{
			name: "Safe animated film",
			item: map[string]any{
				"adult":        false,
				"title":        "Toy Story",
				"vote_count":   15000,
				"vote_average": 8.3,
				"genre_ids":    []any{16, 10751},
			},
			expectedAdult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isAdult := engine.Evaluate(tt.item)
			if isAdult != tt.expectedAdult {
				t.Errorf("expected isAdult=%v, got %v", tt.expectedAdult, isAdult)
			}
		})
	}
}

func TestAdultDecisionTree_ExportAndReload(t *testing.T) {
	engine := NewAdultDecisionEngine()
	tree := engine.GetTree()
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}

	keywords := engine.GetKeywords()
	if len(keywords) == 0 {
		t.Fatal("expected keywords to not be empty")
	}

	engine.SetTree(tree)
	engine.SetKeywords(keywords)
}
