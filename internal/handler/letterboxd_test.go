package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestFindLetterboxdCollectionEscaped(t *testing.T) {
	queries := []string{
		"Top Horror Masterpieces",
		"Top%20Horror%20Masterpieces",
		"top-horror-masterpieces",
		"TOP HORROR MASTERPIECES",
	}

	for _, q := range queries {
		col := FindLetterboxdCollection(q)
		if col == nil {
			t.Fatalf("FindLetterboxdCollection(%q) returned nil", q)
		}
		cMap, ok := col["collection"].(map[string]any)
		if !ok {
			t.Fatalf("FindLetterboxdCollection(%q) missing collection map", q)
		}
		name, _ := cMap["name"].(string)
		if name != "Top Horror Masterpieces" {
			t.Errorf("Expected 'Top Horror Masterpieces', got %q", name)
		}
		movies, _ := cMap["movies"].([]any)
		if len(movies) == 0 {
			t.Errorf("Expected movies in collection, got 0")
		}
	}
}

func TestGetPublicCollectionLetterboxd(t *testing.T) {
	r := chi.NewRouter()
	colHandler := NewCollectionHandler(nil, "test_secret")
	colHandler.RegisterRoutes(r)

	req := httptest.NewRequest("GET", "/collection/letterboxd/Top%20Horror%20Masterpieces", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var res []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("Expected non-empty array of collections")
	}
	col := res[0]
	if col["name"] != "Top Horror Masterpieces" {
		t.Errorf("Expected 'Top Horror Masterpieces', got %v", col["name"])
	}
	movies, ok := col["movies"].([]any)
	if !ok || len(movies) == 0 {
		t.Errorf("Expected movies array in response, got %v", col["movies"])
	}
}

func TestGetLetterboxdCollectionsPagination(t *testing.T) {
	r := chi.NewRouter()
	contentHandler := NewContentHandler(nil, nil, nil, nil, "test_secret")
	contentHandler.RegisterRoutes(r)

	// Page 1
	req1 := httptest.NewRequest("GET", "/collections/letterboxd?page=1&limit=4", nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w1.Code)
	}
	var res1 []map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &res1)
	if len(res1) == 0 {
		t.Fatalf("Expected page 1 results")
	}

	// Page 2
	req2 := httptest.NewRequest("GET", "/collections/letterboxd?page=2&limit=4", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w2.Code)
	}
	var res2 []map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if len(res2) == 0 {
		t.Fatalf("Expected page 2 results")
	}

	// Make sure page 1 and page 2 are different collections
	slug1, _ := res1[0]["slug"].(string)
	slug2, _ := res2[0]["slug"].(string)
	if slug1 == slug2 {
		t.Errorf("Page 1 and Page 2 first items should differ: %s == %s", slug1, slug2)
	}
}
