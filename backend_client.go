package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type BackendClient struct {
	baseURL string
	client  *http.Client
}

func NewBackendClient(cfg Config) *BackendClient {
	return &BackendClient{
		baseURL: cfg.BackendBaseURL,
		client:  &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

func (c *BackendClient) PostForm(ctx context.Context, path string, form map[string]string, apiKey string) (json.RawMessage, map[string]any, error) {
	values := url.Values{}
	for key, value := range form {
		values.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "curl/8.7.1")
	applyAPIKey(req, apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return json.RawMessage(raw), nil, fmt.Errorf("backend %s returned HTTP %d apikey_present=%t: %s", path, resp.StatusCode, strings.TrimSpace(apiKey) != "", string(raw))
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(raw), nil, fmt.Errorf("backend %s returned invalid JSON: %w", path, err)
	}
	return json.RawMessage(raw), decoded, nil
}

func (c *BackendClient) GetTask(ctx context.Context, taskID string, apiKey string) (json.RawMessage, map[string]any, error) {
	values := url.Values{}
	values.Set("task_id", taskID)
	if strings.TrimSpace(apiKey) != "" {
		values.Set("apikey", strings.TrimSpace(apiKey))
	}
	u := c.baseURL + "/api/public/task?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "curl/8.7.1")
	applyAPIKey(req, apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return json.RawMessage(raw), nil, fmt.Errorf("backend task query returned HTTP %d apikey_present=%t: %s", resp.StatusCode, strings.TrimSpace(apiKey) != "", string(raw))
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(raw), nil, err
	}
	return json.RawMessage(raw), decoded, nil
}

func applyAPIKey(req *http.Request, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	req.Header.Set("Apikey", apiKey)
	req.Header.Set("X-API-KEY", apiKey)
}

func backendTaskID(resp map[string]any) string {
	for _, key := range []string{"task_id", "uuid"} {
		if value, ok := resp[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func backendStatus(resp map[string]any) int {
	value, ok := resp["status"]
	if !ok {
		return StatusRunning
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		switch strings.TrimSpace(typed) {
		case "-1":
			return StatusFailed
		case "0":
			return StatusPending
		case "1":
			return StatusRunning
		case "2":
			return StatusSuccess
		case "3":
			return 3
		}
	}
	return StatusRunning
}
