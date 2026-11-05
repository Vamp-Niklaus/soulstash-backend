package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxSearchPageBytes = 8 << 20

// Video is the small subset of YouTube search metadata needed by the player
// source matcher.
type Video struct {
	ID       string
	Title    string
	Channel  string
	Seconds  int
	Verified bool
}

var searchHTTPClient = &http.Client{Timeout: 8 * time.Second}

// Search reads YouTube's public search results page. It needs no API key and
// intentionally returns only videos for which YouTube exposes a duration.
func Search(ctx context.Context, query string) ([]Video, error) {
	searchURL := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,hi;q=0.7")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")

	resp, err := searchHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("YouTube search returned HTTP %d", resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchPageBytes))
	if err != nil {
		return nil, err
	}
	return parseSearchPage(page)
}

func parseSearchPage(page []byte) ([]Video, error) {
	marker := strings.Index(string(page), "ytInitialData")
	if marker < 0 {
		return nil, errors.New("YouTube search page did not include initial data")
	}
	start := strings.IndexByte(string(page[marker:]), '{')
	if start < 0 {
		return nil, errors.New("YouTube search page had malformed initial data")
	}
	start += marker
	end := jsonObjectEnd(page, start)
	if end < 0 {
		return nil, errors.New("YouTube search page had incomplete initial data")
	}

	var payload any
	if err := json.Unmarshal(page[start:end], &payload); err != nil {
		return nil, fmt.Errorf("decode YouTube search data: %w", err)
	}
	results := make([]Video, 0, 15)
	seen := make(map[string]struct{})
	collectVideoRenderers(payload, &results, seen)
	return results, nil
}

func jsonObjectEnd(data []byte, start int) int {
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(data); index++ {
		char := data[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}
	return -1
}

func collectVideoRenderers(value any, results *[]Video, seen map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed["videoRenderer"].(map[string]any); ok {
			if video, valid := parseVideoRenderer(raw); valid {
				if _, exists := seen[video.ID]; !exists {
					seen[video.ID] = struct{}{}
					*results = append(*results, video)
				}
			}
		}
		for _, child := range typed {
			collectVideoRenderers(child, results, seen)
		}
	case []any:
		for _, child := range typed {
			collectVideoRenderers(child, results, seen)
		}
	}
}

func parseVideoRenderer(renderer map[string]any) (Video, bool) {
	id, _ := renderer["videoId"].(string)
	title := rendererText(renderer["title"])
	seconds := parseDuration(rendererText(renderer["lengthText"]))
	if id == "" || title == "" || seconds <= 0 || renderer["isLive"] == true {
		return Video{}, false
	}
	return Video{
		ID:       id,
		Title:    title,
		Channel:  rendererText(renderer["ownerText"]),
		Seconds:  seconds,
		Verified: rendererHasVerifiedBadge(renderer["ownerBadges"]),
	}, true
}

func rendererText(value any) string {
	item, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := item["simpleText"].(string); ok {
		return strings.TrimSpace(text)
	}
	runs, _ := item["runs"].([]any)
	parts := make([]string, 0, len(runs))
	for _, raw := range runs {
		run, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := run["text"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func rendererHasVerifiedBadge(value any) bool {
	badges, _ := value.([]any)
	for _, raw := range badges {
		badge, _ := raw.(map[string]any)
		renderer, _ := badge["metadataBadgeRenderer"].(map[string]any)
		if strings.EqualFold(fmt.Sprint(renderer["style"]), "BADGE_STYLE_TYPE_VERIFIED") {
			return true
		}
	}
	return false
}

func parseDuration(value string) int {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return 0
		}
		values[index] = parsed
	}
	if len(values) == 2 {
		return values[0]*60 + values[1]
	}
	return values[0]*3600 + values[1]*60 + values[2]
}
