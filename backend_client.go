package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var errBackendSubmitOutcomeUnknown = errors.New("backend submit outcome is unknown")

type BackendClient struct {
	baseURL string
	client  *http.Client
}

type BackendHTTPError struct {
	Operation     string
	StatusCode    int
	APIKeyPresent bool
	Body          string
}

func (e *BackendHTTPError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d apikey_present=%t: %s", e.Operation, e.StatusCode, e.APIKeyPresent, e.Body)
}

func NewBackendClient(cfg Config) *BackendClient {
	normalizeConfig(&cfg)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	connectionsPerHost := cfg.WorkerConcurrency + 2
	if connectionsPerHost < 4 {
		connectionsPerHost = 4
	}
	transport.MaxIdleConns = connectionsPerHost * 2
	transport.MaxIdleConnsPerHost = connectionsPerHost
	transport.MaxConnsPerHost = connectionsPerHost
	transport.ResponseHeaderTimeout = cfg.HTTPTimeout

	return &BackendClient{
		baseURL: cfg.BackendBaseURL,
		client: &http.Client{
			Timeout:   cfg.HTTPTimeout,
			Transport: transport,
		},
	}
}

func (c *BackendClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
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
		return nil, nil, fmt.Errorf("%w: sending backend %s: %w", errBackendSubmitOutcomeUnknown, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return json.RawMessage(raw), nil, &BackendHTTPError{
				Operation:     "backend " + path,
				StatusCode:    resp.StatusCode,
				APIKeyPresent: strings.TrimSpace(apiKey) != "",
				Body:          fmt.Sprintf("%s (response read error: %v)", raw, err),
			}
		}
		return json.RawMessage(raw), nil, fmt.Errorf("%w: reading backend %s HTTP %d response: %w", errBackendSubmitOutcomeUnknown, path, resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 {
		return json.RawMessage(raw), nil, &BackendHTTPError{
			Operation:     "backend " + path,
			StatusCode:    resp.StatusCode,
			APIKeyPresent: strings.TrimSpace(apiKey) != "",
			Body:          string(raw),
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(raw), nil, fmt.Errorf("%w: backend %s returned invalid JSON: %w", errBackendSubmitOutcomeUnknown, path, err)
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
		return json.RawMessage(raw), nil, &BackendHTTPError{
			Operation:     "backend task query",
			StatusCode:    resp.StatusCode,
			APIKeyPresent: strings.TrimSpace(apiKey) != "",
			Body:          string(raw),
		}
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
	status, _ := parseBackendStatus(resp)
	return status
}

func parseBackendStatus(resp map[string]any) (int, bool) {
	value, ok := resp["status"]
	if !ok {
		return 0, false
	}
	var status int
	switch typed := value.(type) {
	case float64:
		status = int(typed)
		if float64(status) != typed {
			return 0, false
		}
	case int:
		status = typed
	case string:
		switch strings.TrimSpace(typed) {
		case "-1":
			status = StatusFailed
		case "0":
			status = StatusPending
		case "1":
			status = StatusRunning
		case "2":
			status = StatusSuccess
		case "3":
			status = 3
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	if status != StatusFailed && status != StatusPending && status != StatusRunning && status != StatusSuccess && status != 3 {
		return 0, false
	}
	return status, true
}
