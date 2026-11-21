package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// TMDB video endpoints used for trailers. The response is TMDB's own JSON
// ({"id": ..., "results": [{"key","site","type","size","official","name",...}]});
// the SPA picks the best YouTube trailer from it.

// videoPathID returns a URL param only if it is a non-negative integer, so it is safe to put in a TMDB path.
func videoPathID(r *http.Request, name string) (string, bool) {
	raw := chi.URLParam(r, name)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return "", false
	}
	return strconv.Itoa(n), true
}

func (h *ContentHandler) proxyVideos(w http.ResponseWriter, r *http.Request, endpoint string) {
	// Include videos with no language set as well as English ones.
	h.fetchProxyWithParams(w, r, endpoint, map[string]string{
		"language":               "en-US",
		"include_video_language": "en,null",
	})
}

func (h *ContentHandler) GetMovieVideos(w http.ResponseWriter, r *http.Request) {
	id, ok := videoPathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid id")
		return
	}
	h.proxyVideos(w, r, fmt.Sprintf("/3/movie/%s/videos", id))
}

func (h *ContentHandler) GetSeriesVideos(w http.ResponseWriter, r *http.Request) {
	id, ok := videoPathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid id")
		return
	}
	h.proxyVideos(w, r, fmt.Sprintf("/3/tv/%s/videos", id))
}

func (h *ContentHandler) GetSeasonVideos(w http.ResponseWriter, r *http.Request) {
	id, ok := videoPathID(r, "id")
	season, ok2 := videoPathID(r, "seasonNumber")
	if !ok || !ok2 {
		writeError(w, http.StatusBadRequest, "Invalid id or season")
		return
	}
	h.proxyVideos(w, r, fmt.Sprintf("/3/tv/%s/season/%s/videos", id, season))
}

func (h *ContentHandler) GetEpisodeVideos(w http.ResponseWriter, r *http.Request) {
	id, ok := videoPathID(r, "id")
	season, ok2 := videoPathID(r, "seasonNumber")
	episode, ok3 := videoPathID(r, "episodeNumber")
	if !ok || !ok2 || !ok3 {
		writeError(w, http.StatusBadRequest, "Invalid id, season or episode")
		return
	}
	h.proxyVideos(w, r, fmt.Sprintf("/3/tv/%s/season/%s/episode/%s/videos", id, season, episode))
}
