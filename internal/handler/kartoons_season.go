package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Season merge for GET /api/series/:id/season/:seasonNumber.
//
// Series match: normalized titles must be exactly equal (text in parentheses
// counts as an alternate title, e.g. "Pakdam Pakdai (Rat-A-Tat)" matches both
// "Pakdam Pakdai" and "Rat-A-Tat"). Years are compared only when both sides have one.
//
// Episode union per season:
//   - The source with more episodes that season is the priority source; ties go to TMDB.
//   - Same episode number and same normalized title: one entry, with fields from the
//     priority source and any empty field filled from the other, plus both stream sources.
//   - Same episode number, different title: both are kept as separate entries.
//   - Episode number present in only one source: that episode is kept.
//
// Ids: SxEy-tmdb, SxEy-kartoon, or SxEy-<priority> for merged entries.

var ksHTTP = &http.Client{Timeout: 10 * time.Second}

var (
	ksNonAlnum     = regexp.MustCompile(`[^a-z0-9]+`)
	ksStopwords    = regexp.MustCompile(`\b(the|a|an)\b`)
	ksParen        = regexp.MustCompile(`\(([^)]*)\)`)
	ksSxxExxSuffix = regexp.MustCompile(`(?i)\s*S\d+E\d+.*$`)
	ksSeasonSuffix = regexp.MustCompile(`(?i)\s*Season\s*\d+.*$`)
)

// seriesSeasonWithKartoons replaces the plain TMDB season proxy.
func (h *ContentHandler) seriesSeasonWithKartoons(w http.ResponseWriter, r *http.Request, id, season string) {
	seasonNumber, err := strconv.Atoi(season)
	if err != nil || seasonNumber <= 0 {
		h.fetchProxy(w, r, fmt.Sprintf("/3/tv/%s/season/%s", id, season))
		return
	}

	ctx := r.Context()
	data, err := h.tmdbClient.Get(ctx, fmt.Sprintf("/3/tv/%s/season/%d", id, seasonNumber), requestQueryParams(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "Season not found")
		return
	}

	rawEps, _ := data["episodes"].([]any)
	tmdbEps := ksMaps(rawEps)

	q := r.URL.Query()
	year := ksYear(q.Get("year"))
	names := []string{strings.TrimSpace(q.Get("title"))}
	if tv, tvErr := h.tmdbClient.Get(ctx, "/3/tv/"+id, map[string]string{}); tvErr == nil {
		names = append(names, ksString(tv["name"]), ksString(tv["original_name"]))
		if year == 0 {
			year = ksYear(ksString(tv["first_air_date"]))
		}
	}

	tmdbEps = ksMergeSeason(ctx, tmdbEps, names, year, seasonNumber)

	episodes := make([]any, 0, len(tmdbEps))
	for _, ep := range tmdbEps {
		episodes = append(episodes, ep)
	}
	data["episodes"] = episodes
	writeJSON(w, http.StatusOK, data)
}

// ksMergeSeason finds the Kartoons show and unions its season episodes with TMDB's.
// Any failure returns the TMDB episodes unchanged.
func ksMergeSeason(ctx context.Context, tmdbEps []map[string]any, names []string, year, seasonNumber int) []map[string]any {
	show, ok := ksFindShow(ctx, names, year)
	if !ok {
		return tmdbEps
	}
	slug := ksString(show["slug"])

	kEps := ksSeasonEpisodes(ctx, slug, seasonNumber)
	if len(kEps) == 0 {
		kEps = ksFallbackSeasonEpisodes(ctx, slug, seasonNumber)
	}
	if len(kEps) == 0 {
		return tmdbEps
	}
	return ksUnionEpisodes(tmdbEps, kEps, slug, seasonNumber)
}

// ksFindShow searches Kartoons by each title and returns the first show that matches exactly.
func ksFindShow(ctx context.Context, names []string, year int) (map[string]any, bool) {
	seenTerm := map[string]bool{}
	var terms []string
	for _, n := range names {
		c := ksCleanTitle(n)
		if c == "" || seenTerm[c] {
			continue
		}
		seenTerm[c] = true
		terms = append(terms, c)
	}

	seenSlug := map[string]bool{}
	for _, term := range terms {
		payload, ok := ksGet(ctx, "https://api.kartoons.me/api/shows?search="+url.QueryEscape(term))
		if !ok {
			continue
		}
		for _, c := range ksItems(payload, "data") {
			slug := ksString(c["slug"])
			if slug == "" || seenSlug[slug] {
				continue
			}
			seenSlug[slug] = true
			if ksShowMatches(c, names, year) {
				return c, true
			}
		}
	}
	return nil, false
}

func ksShowMatches(c map[string]any, names []string, year int) bool {
	kYear := ksYear(ksFirstNonEmpty(c["startYear"], c["releaseYear"]))
	if year != 0 && kYear != 0 && year != kYear {
		return false
	}
	kTitles := ksTitleVariants(ksString(c["title"]))
	for _, n := range names {
		nn := ksNormalize(n)
		if nn == "" {
			continue
		}
		for _, kt := range kTitles {
			if kt != "" && kt == nn {
				return true
			}
		}
	}
	return false
}

// ksTitleVariants returns the full title, the title without parentheses, and the text inside them.
func ksTitleVariants(title string) []string {
	out := []string{ksNormalize(title)}
	if m := ksParen.FindStringSubmatch(title); m != nil {
		out = append(out, ksNormalize(m[1]))
		out = append(out, ksNormalize(ksParen.ReplaceAllString(title, "")))
	}
	return out
}

// ksUnionEpisodes merges TMDB and Kartoons episodes for one season.
func ksUnionEpisodes(tmdbEps []map[string]any, kEps []map[string]any, slug string, seasonNumber int) []map[string]any {
	tByNum := map[int]map[string]any{}
	for _, t := range tmdbEps {
		n := int(ksNumber(t["episode_number"]))
		if n <= 0 {
			continue
		}
		if _, exists := tByNum[n]; !exists {
			tByNum[n] = t
		}
	}

	kByNum := map[int]map[string]any{}
	var kNoNumber []map[string]any
	for _, k := range kEps {
		n := int(ksNumber(k["episodeNumber"]))
		if n <= 0 {
			kNoNumber = append(kNoNumber, k)
			continue
		}
		if _, exists := kByNum[n]; !exists {
			kByNum[n] = k
		}
	}

	tmdbPriority := len(tmdbEps) >= len(kEps)

	nums := map[int]bool{}
	for n := range tByNum {
		nums[n] = true
	}
	for n := range kByNum {
		nums[n] = true
	}
	sorted := make([]int, 0, len(nums))
	for n := range nums {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)

	out := make([]map[string]any, 0, len(sorted)+len(kNoNumber))
	for _, n := range sorted {
		t, hasT := tByNum[n]
		k, hasK := kByNum[n]

		switch {
		case hasT && hasK:
			tTitle := ksNormalize(ksString(t["name"]))
			kTitle := ksNormalize(ksString(k["title"]))
			if tTitle != "" && tTitle == kTitle {
				out = append(out, ksBuildMerged(t, k, tmdbPriority, slug, seasonNumber, n))
			} else if tmdbPriority {
				out = append(out, ksBuildTmdb(t, seasonNumber, n), ksBuildKartoon(k, slug, seasonNumber, n))
			} else {
				out = append(out, ksBuildKartoon(k, slug, seasonNumber, n), ksBuildTmdb(t, seasonNumber, n))
			}
		case hasT:
			out = append(out, ksBuildTmdb(t, seasonNumber, n))
		default:
			out = append(out, ksBuildKartoon(k, slug, seasonNumber, n))
		}
	}

	// Kartoons episodes without a usable number are appended so they are not lost.
	for i, k := range kNoNumber {
		entry := ksBuildKartoon(k, slug, seasonNumber, 0)
		entry["id"] = fmt.Sprintf("S%d-kartoon-x%d", seasonNumber, i+1)
		entry["episode_number"] = nil
		out = append(out, entry)
	}

	return out
}

func ksBuildTmdb(t map[string]any, seasonNumber, n int) map[string]any {
	entry := make(map[string]any, len(t)+6)
	for k, v := range t {
		entry[k] = v
	}
	entry["tmdb_episode_id"] = t["id"]
	entry["id"] = fmt.Sprintf("S%dE%d-tmdb", seasonNumber, n)
	entry["episode_number"] = n
	entry["season_number"] = seasonNumber
	entry["source"] = "tmdb"
	entry["has_kartoons"] = false
	return entry
}

func ksBuildKartoon(k map[string]any, slug string, seasonNumber, n int) map[string]any {
	title := ksString(k["title"])
	name := title
	if name == "" {
		name = fmt.Sprintf("Episode %d", n)
	}
	return map[string]any{
		"id":                  fmt.Sprintf("S%dE%d-kartoon", seasonNumber, n),
		"episode_number":      n,
		"season_number":       seasonNumber,
		"name":                name,
		"overview":            ksString(k["description"]),
		"still_path":          ksFirstNonEmpty(k["thumbnail"], k["image"]),
		"air_date":            ksString(k["airDate"]),
		"runtime":             nil,
		"vote_average":        ksNumber(k["rating"]),
		"source":              "kartoon",
		"has_kartoons":        true,
		"kartoons_episode_id": ksString(k["_id"]),
		"kartoons_slug":       slug,
		"kartoons_title":      title,
	}
}

// ksBuildMerged keeps the priority source's fields and fills empty ones from the other source.
func ksBuildMerged(t, k map[string]any, tmdbPriority bool, slug string, seasonNumber, n int) map[string]any {
	tEntry := ksBuildTmdb(t, seasonNumber, n)
	kEntry := ksBuildKartoon(k, slug, seasonNumber, n)

	for _, field := range []string{"name", "overview", "still_path", "air_date", "runtime", "vote_average"} {
		tv, kv := tEntry[field], kEntry[field]
		if tmdbPriority {
			if ksIsEmpty(tv) {
				tEntry[field] = kv
			}
		} else if !ksIsEmpty(kv) {
			tEntry[field] = kv
		} else {
			tEntry[field] = tv
		}
	}

	priority := "kartoon"
	if tmdbPriority {
		priority = "tmdb"
	}
	tEntry["id"] = fmt.Sprintf("S%dE%d-%s", seasonNumber, n, priority)
	tEntry["source"] = "merged"
	tEntry["priority_source"] = priority
	tEntry["has_kartoons"] = true
	tEntry["kartoons_episode_id"] = kEntry["kartoons_episode_id"]
	tEntry["kartoons_slug"] = slug
	tEntry["kartoons_title"] = kEntry["kartoons_title"]
	return tEntry
}

// ksFallbackSeasonEpisodes uses the season that has episodes when the requested season is empty.
func ksFallbackSeasonEpisodes(ctx context.Context, slug string, seasonNumber int) []map[string]any {
	detail, ok := ksGet(ctx, "https://api.kartoons.me/api/shows/"+url.PathEscape(slug))
	if !ok {
		return nil
	}
	obj, _ := detail.(map[string]any)
	if inner, ok := obj["data"].(map[string]any); ok {
		obj = inner
	}
	seasons := ksItems(obj["seasons"])

	var target map[string]any
	for _, s := range seasons {
		if ksNumber(s["seasonNumber"]) == float64(seasonNumber) {
			target = s
			break
		}
	}
	if target == nil {
		for _, s := range seasons {
			if ksNumber(s["episodeCount"]) > 0 {
				target = s
				break
			}
		}
	}
	if target == nil {
		return nil
	}
	sNum := int(ksNumber(target["seasonNumber"]))
	if sNum == 0 {
		sNum = 1
	}
	return ksSeasonEpisodes(ctx, slug, sNum)
}

func ksSeasonEpisodes(ctx context.Context, slug string, seasonNumber int) []map[string]any {
	payload, ok := ksGet(ctx, fmt.Sprintf("https://api.kartoons.me/api/shows/%s/season/season-%d/all-episodes", url.PathEscape(slug), seasonNumber))
	if !ok {
		return nil
	}
	return ksItems(payload, "data", "episodes")
}

// ksNormalize lowercases, folds punctuation and "&", and drops articles.
// ASCII-only: accented letters are removed rather than transliterated.
func ksNormalize(s string) string {
	v := strings.ToLower(s)
	v = strings.ReplaceAll(v, "&", " and ")
	v = ksNonAlnum.ReplaceAllString(v, " ")
	v = ksStopwords.ReplaceAllString(v, " ")
	return strings.Join(strings.Fields(v), " ")
}

// ksCleanTitle strips episode and season suffixes from a series title.
func ksCleanTitle(title string) string {
	v := ksSxxExxSuffix.ReplaceAllString(title, "")
	v = ksSeasonSuffix.ReplaceAllString(v, "")
	return strings.Join(strings.Fields(v), " ")
}

func ksGet(ctx context.Context, rawURL string) (any, bool) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")

	resp, err := ksHTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, false
	}
	return v, true
}

// ksItems accepts either a bare array or an object holding an array under one of keys.
func ksItems(payload any, keys ...string) []map[string]any {
	if arr, ok := payload.([]any); ok {
		return ksMaps(arr)
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range keys {
		if arr, ok := obj[k].([]any); ok {
			return ksMaps(arr)
		}
	}
	return nil
}

func ksMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ksString mirrors JavaScript String() for the JSON values these APIs return.
func ksString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// ksNumber mirrors JavaScript Number() with 0 for missing or non-numeric values.
func ksNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func ksYear(s string) int {
	if len(s) >= 4 {
		if n, err := strconv.Atoi(s[:4]); err == nil {
			return n
		}
	}
	return 0
}

func ksIsEmpty(v any) bool {
	return v == nil || ksString(v) == ""
}

func ksFirstNonEmpty(vals ...any) string {
	for _, v := range vals {
		if s := ksString(v); s != "" {
			return s
		}
	}
	return ""
}
