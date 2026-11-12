package service

import (
	"math"
	"regexp"
	"strings"
)

var (
	IndianLanguages = map[string]bool{
		"hi": true, "ta": true, "te": true, "ml": true, "kn": true,
		"bn": true, "mr": true, "gu": true, "pa": true, "ur": true,
	}
	nonAlphaNumRegex = regexp.MustCompile(`[^a-z0-9 ]`)
	spacesRegex      = regexp.MustCompile(`\s+`)
)

func normalizeSearchText(str string) string {
	lower := strings.ToLower(str)
	clean := nonAlphaNumRegex.ReplaceAllString(lower, "")
	clean = spacesRegex.ReplaceAllString(clean, " ")
	return strings.TrimSpace(clean)
}

// ComputeFinalScore mirrors Node util.js:67-107
func ComputeFinalScore(item map[string]any, query string, yearQuery int) float64 {
	var score float64

	q := strings.ToLower(query)
	title, _ := item["title"].(string)
	t1 := strings.ToLower(title)
	origTitle, _ := item["originalTitle"].(string)
	if origTitle == "" {
		origTitle, _ = item["original_title"].(string)
	}
	t2 := strings.ToLower(origTitle)

	// Exact title match (highest priority)
	if t1 == q || t2 == q {
		score += 200
	} else if strings.Contains(t1, q) || strings.Contains(t2, q) {
		score += 100
	} else {
		// Partial word match
		t1Words := strings.Fields(t1)
		t2Words := strings.Fields(t2)
		matched := false
		for _, w := range t1Words {
			if strings.Contains(w, q) {
				matched = true
				break
			}
		}
		if !matched {
			for _, w := range t2Words {
				if strings.Contains(w, q) {
					matched = true
					break
				}
			}
		}
		if matched {
			score += 40
		}
	}

	// Penalty for missing critical data
	if title == "" {
		score -= 50
	}
	if poster, ok := item["poster_path"].(string); !ok || poster == "" {
		score -= 30
	}

	// Year match
	var releaseYear int
	if ry, ok := item["release_year"].(int); ok {
		releaseYear = ry
	} else if rd, ok := item["release_date"].(string); ok && len(rd) >= 4 {
		var yr int
		for i := 0; i < 4; i++ {
			if rd[i] >= '0' && rd[i] <= '9' {
				yr = yr*10 + int(rd[i]-'0')
			}
		}
		if yr > 1800 {
			releaseYear = yr
		}
	}

	if yearQuery > 0 && releaseYear == yearQuery {
		score += 70 // HARD BOOST
	}

	// Indian priority
	if countries, ok := item["originCountry"].([]string); ok {
		for _, c := range countries {
			if c == "IN" {
				score += 50
				break
			}
		}
	} else if countriesAny, ok := item["origin_country"].([]any); ok {
		for _, c := range countriesAny {
			if cStr, ok := c.(string); ok && cStr == "IN" {
				score += 50
				break
			}
		}
	}

	origLang, _ := item["originalLanguage"].(string)
	if origLang == "" {
		origLang, _ = item["original_language"].(string)
	}
	if IndianLanguages[origLang] {
		score += 15
	}

	// Credibility
	votes := getFloat(item["voteCount"])
	if votes == 0 {
		votes = getFloat(item["vote_count"])
	}
	score += math.Min(votes/200, 20)

	avg := getFloat(item["voteAverage"])
	if avg == 0 {
		avg = getFloat(item["vote_average"])
	}
	score += avg

	return score
}

// NormalizeMediaItem mirrors Node util.js:109-143
func NormalizeMediaItem(item map[string]any, mediaType string) map[string]any {
	isMovie := mediaType == "movie"

	releaseDate, _ := item["release_date"].(string)
	if !isMovie {
		releaseDate, _ = item["first_air_date"].(string)
	}

	var releaseYear *int
	if len(releaseDate) >= 4 {
		var yearVal int
		for i := 0; i < 4; i++ {
			if releaseDate[i] >= '0' && releaseDate[i] <= '9' {
				yearVal = yearVal*10 + int(releaseDate[i]-'0')
			}
		}
		if yearVal > 1800 {
			releaseYear = &yearVal
		}
	}

	mediaTypeLabel := "Movie"
	if !isMovie {
		mediaTypeLabel = "Series"
	}

	title, _ := item["title"].(string)
	if title == "" {
		title, _ = item["name"].(string)
	}

	origTitle, _ := item["original_title"].(string)
	if origTitle == "" {
		origTitle, _ = item["original_name"].(string)
	}

	return map[string]any{
		"id":               item["id"],
		"type":             mediaType,
		"media_type":       mediaTypeLabel,
		"title":            title,
		"originalTitle":    origTitle,
		"poster_path":      item["poster_path"],
		"backdrop_path":    item["backdrop_path"],
		"release_date":     releaseDate,
		"release_year":     releaseYear,
		"originalLanguage": item["original_language"],
		"originCountry":    item["origin_country"],
		"genre_ids":        item["genre_ids"],
		"voteCount":        item["vote_count"],
		"voteAverage":      item["vote_average"],
		"popularity":       item["popularity"],
	}
}
