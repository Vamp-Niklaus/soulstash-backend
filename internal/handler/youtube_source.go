package handler

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"soulstash/internal/platform/mongodb"
	"soulstash/internal/platform/youtube"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const minimumYouTubeMovieSeconds = 20 * 60

type youtubeIdentity struct {
	mediaType      string
	title          string
	originalTitle  string
	episodeTitle   string
	year           string
	actors         []string
	runtimeMinutes float64
	seasonNumber   int
	episodeNumber  int
}

func (h *ContentHandler) findFullYouTubeVideo(ctx context.Context, tmdbID, mediaType string, season, episode int) (*youtube.Video, youtubeIdentity) {
	identity, ok := h.loadYouTubeIdentity(ctx, tmdbID, mediaType, season, episode)
	if !ok {
		return nil, identity
	}
	queries := buildYouTubeQueries(identity)
	if len(queries) == 0 {
		return nil, identity
	}

	searchCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	var best *youtube.Video
	bestScore := -1.0
	for queryIndex, query := range queries {
		videos, err := youtube.Search(searchCtx, query)
		if err != nil {
			if searchCtx.Err() != nil {
				break
			}
			continue
		}
		for index := range videos {
			video := &videos[index]
			if !youtubeVideoMatches(*video, identity) {
				continue
			}
			score := scoreYouTubeVideo(*video, identity, queryIndex, len(queries))
			if score > bestScore || (score == bestScore && best != nil && video.Seconds > best.Seconds) {
				best, bestScore = video, score
			}
		}
		// Keep Node's first-query confidence short-circuit after scoring all
		// candidates from that most specific query.
		if queryIndex == 0 && best != nil && bestScore >= 80 {
			break
		}
	}
	if best == nil {
		return nil, identity
	}
	return best, identity
}

func (h *ContentHandler) loadYouTubeIdentity(ctx context.Context, tmdbID, mediaType string, season, episode int) (youtubeIdentity, bool) {
	identity := youtubeIdentity{mediaType: mediaType, seasonNumber: season, episodeNumber: episode}
	endpoint := "/3/movie/" + tmdbID
	if mediaType == "series" {
		endpoint = "/3/tv/" + tmdbID
	}
	detail, err := h.tmdbClient.Get(ctx, endpoint, map[string]string{"append_to_response": "credits", "language": "en-US"})
	if err != nil || detail == nil {
		return identity, false
	}
	if mediaType == "series" {
		identity.title = firstNonEmptyString(detail, "name", "original_name")
		identity.originalTitle = firstNonEmptyString(detail, "original_name", "name")
		identity.year = firstDateYear(firstNonEmptyString(detail, "first_air_date"))
		identity.runtimeMinutes = firstRuntime(detail["episode_run_time"])
		credits := asContentMap(detail["credits"])
		identity.actors = topYouTubeCast(credits["cast"])

		episodeDetail, episodeErr := h.tmdbClient.Get(ctx,
			fmt.Sprintf("/3/tv/%s/season/%d/episode/%d", tmdbID, season, episode),
			map[string]string{"language": "en-US"},
		)
		if episodeErr == nil && episodeDetail != nil {
			identity.episodeTitle = firstNonEmptyString(episodeDetail, "name", "original_name")
			if runtime := getFloat(episodeDetail["runtime"]); runtime > 0 {
				identity.runtimeMinutes = runtime
			}
		}
	} else {
		identity.title = firstNonEmptyString(detail, "title", "original_title")
		identity.originalTitle = firstNonEmptyString(detail, "original_title", "title")
		identity.year = firstDateYear(firstNonEmptyString(detail, "release_date"))
		identity.runtimeMinutes = getFloat(detail["runtime"])
		credits := asContentMap(detail["credits"])
		identity.actors = topYouTubeCast(credits["cast"])
	}
	return identity, strings.TrimSpace(identity.title) != ""
}

func buildYouTubeQueries(identity youtubeIdentity) []string {
	title := strings.TrimSpace(identity.title)
	if title == "" {
		return nil
	}
	titleYear := strings.TrimSpace(strings.Join([]string{title, identity.year}, " "))
	actorText := strings.Join(identity.actors, " and ")
	queries := make([]string, 0, 5)
	if identity.mediaType == "movie" {
		if actorText != "" {
			queries = append(queries, fmt.Sprintf("%s hindi %s full movie hd 1080", title, actorText))
		} else {
			queries = append(queries, fmt.Sprintf("%s full movie hindi 1080p", titleYear))
		}
		queries = append(queries,
			fmt.Sprintf("%s full movie hindi dubbed", titleYear),
			fmt.Sprintf("%s full movie 1080p", titleYear),
			fmt.Sprintf("%s full movie", titleYear),
			fmt.Sprintf("%s full movie hindi", title),
		)
		if identity.originalTitle != "" && normalizeYouTubeText(identity.originalTitle) != normalizeYouTubeText(title) {
			queries = append(queries, fmt.Sprintf("%s %s full movie hindi hd", identity.originalTitle, identity.year))
		}
	} else {
		seasonEpisode := fmt.Sprintf("S%02dE%02d", identity.seasonNumber, identity.episodeNumber)
		episodeQueryTitle := title
		if identity.episodeTitle != "" {
			episodeQueryTitle += " " + identity.episodeTitle
		}
		queries = append(queries,
			fmt.Sprintf("%s %s hindi dubbed full episode", titleYear, seasonEpisode),
			fmt.Sprintf("%s episode %d hindi dubbed full episode", episodeQueryTitle, identity.episodeNumber),
			fmt.Sprintf("%s season %d episode %d hindi full episode", title, identity.seasonNumber, identity.episodeNumber),
			fmt.Sprintf("%s %s full episode", title, seasonEpisode),
			fmt.Sprintf("%s episode %d full episode", episodeQueryTitle, identity.episodeNumber),
		)
	}
	for index := range queries {
		queries[index] = strings.Join(strings.Fields(queries[index]), " ")
	}
	return queries
}

func firstDateYear(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 4 {
		return value[:4]
	}
	return ""
}

func firstRuntime(value any) float64 {
	values, _ := value.([]any)
	for _, item := range values {
		if runtime := getFloat(item); runtime > 0 {
			return runtime
		}
	}
	return 0
}

func topYouTubeCast(value any) []string {
	cast, _ := value.([]any)
	actors := make([]string, 0, 2)
	for _, raw := range cast {
		person, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(person["name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		actors = append(actors, name)
		if len(actors) == 2 {
			break
		}
	}
	return actors
}

func youtubeVideoMatches(video youtube.Video, identity youtubeIdentity) bool {
	if video.Seconds < minimumYouTubeMovieSeconds {
		return false
	}
	if identity.runtimeMinutes > 0 && float64(video.Seconds)/60 < identity.runtimeMinutes*0.75 {
		return false
	}
	if identity.runtimeMinutes > 0 && float64(video.Seconds)/60 > identity.runtimeMinutes*2.5 {
		return false
	}
	title := normalizeYouTubeText(video.Title)
	for _, blocked := range []string{
		"trailer", "teaser", "promo", "clip", "review", "reaction", "recap", "explained",
		"movie scenes", "best scenes", "video song", "jukebox", "short film", "making of",
	} {
		if hasYouTubePhrase(title, blocked) {
			return false
		}
	}
	return youtubeTitleMatch(video.Title, identity.title) || youtubeTitleMatch(video.Title, identity.originalTitle)
}

func scoreYouTubeVideo(video youtube.Video, identity youtubeIdentity, queryIndex, queryCount int) float64 {
	title := normalizeYouTubeText(video.Title)
	score := float64((queryCount - queryIndex) * 4)
	similarity := youtubeTitleSimilarity(video.Title, identity.title)
	if originalSimilarity := youtubeTitleSimilarity(video.Title, identity.originalTitle); originalSimilarity > similarity {
		similarity = originalSimilarity
	}
	score += similarity * 100
	if strings.Contains(title, "hindi dubbed") {
		score += 50
	} else if hasYouTubePhrase(title, "hindi") {
		score += 20
	}
	if strings.Contains(title, "1080p") || strings.Contains(title, "1080 p") || hasYouTubePhrase(title, "hd") {
		score += 30
	}
	if hasYouTubePhrase(title, "full movie") || hasYouTubePhrase(title, "full episode") || hasYouTubePhrase(title, "complete movie") {
		score += 10
	}
	for _, actor := range identity.actors {
		if strings.Contains(title, normalizeYouTubeText(actor)) {
			score += 12
		}
	}
	if video.Verified {
		score += 15
	}
	if identity.runtimeMinutes > 0 {
		difference := absFloat(float64(video.Seconds)/60 - identity.runtimeMinutes)
		score += maxFloat(0, 30-difference)
	} else if video.Seconds >= 3600 {
		score += 25
	} else if video.Seconds >= 1800 {
		score += 12
	}
	return score
}

func youtubeTitleMatch(videoTitle, targetTitle string) bool {
	video := normalizeYouTubeText(videoTitle)
	target := normalizeYouTubeText(targetTitle)
	if target == "" {
		return false
	}
	if strings.Contains(video, target) {
		return true
	}
	words := strings.Fields(target)
	if len(words) == 0 {
		return false
	}
	matched := 0
	for _, word := range words {
		if strings.Contains(video, word) {
			matched++
		}
	}
	return float64(matched)/float64(len(words)) >= 0.5
}

func youtubeTitleSimilarity(videoTitle, targetTitle string) float64 {
	words := strings.Fields(normalizeYouTubeText(targetTitle))
	if len(words) == 0 {
		return 0
	}
	video := normalizeYouTubeText(videoTitle)
	matched := 0
	for _, word := range words {
		if strings.Contains(video, word) {
			matched++
		}
	}
	return float64(matched) / float64(len(words))
}

var nonWordYouTubeChars = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func normalizeYouTubeText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Join(strings.Fields(nonWordYouTubeChars.ReplaceAllString(value, " ")), " ")
}

func hasYouTubePhrase(value, phrase string) bool {
	return strings.Contains(" "+value+" ", " "+normalizeYouTubeText(phrase)+" ")
}

func (h *ContentHandler) persistYouTubeSource(ctx context.Context, tmdbID int64, identity youtubeIdentity, video youtube.Video) {
	if h.db == nil || video.ID == "" {
		return
	}
	now := time.Now()
	url := youtubeWatchURL(video.ID)
	filter := bson.M{"tmdbId": tmdbID, "mediaType": identity.mediaType}
	set := bson.M{"updatedAt": now}
	setOnInsert := bson.M{"createdAt": now, "tmdbId": tmdbID, "mediaType": identity.mediaType}
	if year, err := strconv.Atoi(identity.year); err == nil {
		setOnInsert["year"] = year
	}
	if identity.mediaType == "series" {
		episodeKey := fmt.Sprintf("s%de%d", identity.seasonNumber, identity.episodeNumber)
		prefix := "episodes." + episodeKey + "."
		set[prefix+"sources.youtube"] = bson.A{url}
		set[prefix+"youtubeDurationSeconds"] = video.Seconds
		set[prefix+"youtubeTargetRuntimeMinutes"] = identity.runtimeMinutes
		set[prefix+"youtubeTitle"] = video.Title
		set[prefix+"updatedAt"] = now
		setOnInsert["title"] = identity.title
		setOnInsert["episodes."+episodeKey+".seasonNumber"] = identity.seasonNumber
		setOnInsert["episodes."+episodeKey+".episodeNumber"] = identity.episodeNumber
	} else {
		set["sources.youtube"] = bson.A{url}
		set["youtubeDurationSeconds"] = video.Seconds
		set["youtubeTargetRuntimeMinutes"] = identity.runtimeMinutes
		set["youtubeTitle"] = video.Title
		setOnInsert["title"] = identity.title
	}
	_, _ = h.db.Collection(mongodb.CollectionPlayerSources).UpdateOne(ctx, filter, bson.M{
		"$set": set, "$setOnInsert": setOnInsert,
	}, options.UpdateOne().SetUpsert(true))
}

func youtubeWatchURL(videoID string) string {
	return "https://www.youtube-nocookie.com/embed/" + url.PathEscape(videoID)
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func isYouTubeFullSource(sourceRecord map[string]any, runtimeMinutes float64) bool {
	seconds := int(getFloat(sourceRecord["youtubeDurationSeconds"]))
	targetRuntime := getFloat(sourceRecord["youtubeTargetRuntimeMinutes"])
	title := fmt.Sprint(sourceRecord["youtubeTitle"])
	if seconds < minimumYouTubeMovieSeconds || title == "<nil>" || hasYouTubePhrase(normalizeYouTubeText(title), "trailer") {
		return false
	}
	if targetRuntime > 0 && float64(seconds)/60 < targetRuntime*0.75 {
		return false
	}
	if targetRuntime == 0 && runtimeMinutes > 0 && float64(seconds)/60 < runtimeMinutes*0.75 {
		return false
	}
	return true
}
