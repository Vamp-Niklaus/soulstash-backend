package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client interface {
	Get(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error)
	GetRaw(ctx context.Context, endpoint string, queryParams map[string]string) ([]byte, error)
}

type tmdbClient struct {
	baseURL     string
	bearerToken string
	httpClient  *http.Client
}

func NewClient(baseURL, bearerToken string) Client {
	cleanURL := strings.TrimRight(baseURL, "/")
	cleanURL = strings.ReplaceAll(cleanURL, "api.themoviedb.org", "api.tmdb.org")
	return &tmdbClient{
		baseURL:     cleanURL,
		bearerToken: bearerToken,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *tmdbClient) Get(ctx context.Context, endpoint string, queryParams map[string]string) (map[string]any, error) {
	data, err := c.GetRaw(ctx, endpoint, queryParams)
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *tmdbClient) GetRaw(ctx context.Context, endpoint string, queryParams map[string]string) ([]byte, error) {
	url := fmt.Sprintf("%s%s", c.baseURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	for k, v := range queryParams {
		if v != "" {
			q.Set(k, v)
		}
	}
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("TMDB resource not found (404)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("TMDB API error with status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
