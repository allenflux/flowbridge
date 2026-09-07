package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublicTaskDetailsMatchesSingleQueryResponses(t *testing.T) {
	server, store := newBatchTaskTestServer(t)
	createBatchTaskFixture(t, store, "batch-pending", StatusPending, nil)
	createBatchTaskFixture(t, store, "batch-running", StatusRunning, nil)
	createBatchTaskFixture(t, store, "batch-failed", StatusFailed, nil)
	createBatchTaskFixture(t, store, "batch-success", StatusSuccess, json.RawMessage(
		`{"uuid":"batch-success_video","task_id":"batch-success_video","status":2,"out_data":["https://cdn.example/final.mp4"]}`,
	))

	requested := []string{" batch-running ", "missing", "batch-success", "batch-running", "batch-failed", "batch-pending"}
	raw, err := json.Marshal(requested)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, publicTaskDetailsPath, strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}

	var actual []any
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	wantedIDs := []string{"batch-running", "batch-success", "batch-failed", "batch-pending"}
	wanted := make([]any, 0, len(wantedIDs))
	for _, taskID := range wantedIDs {
		singleRequest := httptest.NewRequest(http.MethodGet, "/api/public/task?task_id="+taskID, nil)
		singleResponse := httptest.NewRecorder()
		server.ServeHTTP(singleResponse, singleRequest)
		if singleResponse.Code != http.StatusOK {
			t.Fatalf("single status for %s = %d: %s", taskID, singleResponse.Code, singleResponse.Body.String())
		}
		var item any
		if err := json.Unmarshal(singleResponse.Body.Bytes(), &item); err != nil {
			t.Fatalf("decode single response for %s: %v", taskID, err)
		}
		wanted = append(wanted, item)
	}
	if !reflect.DeepEqual(actual, wanted) {
		t.Fatalf("batch response mismatch\nactual: %#v\nwanted: %#v", actual, wanted)
	}
}

func TestPublicTaskDetailsErrors(t *testing.T) {
	server, _ := newBatchTaskTestServer(t)
	tooManyIDs := make([]string, maxPublicTaskDetailsIDs+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = fmt.Sprintf("task-%04d", index)
	}
	tooManyBody, err := json.Marshal(tooManyIDs)
	if err != nil {
		t.Fatalf("marshal too many ids: %v", err)
	}
	maximumBody, err := json.Marshal(tooManyIDs[:maxPublicTaskDetailsIDs])
	if err != nil {
		t.Fatalf("marshal maximum ids: %v", err)
	}
	oversizedBody := `["` + strings.Repeat("x", (1<<20)+1) + `"]`
	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
		wantError  string
	}{
		{name: "empty array", method: http.MethodPost, body: `[]`, wantStatus: http.StatusBadRequest, wantError: "ids required"},
		{name: "blank ids", method: http.MethodPost, body: `[" ", ""]`, wantStatus: http.StatusBadRequest, wantError: "ids required"},
		{name: "all missing", method: http.MethodPost, body: `["missing"]`, wantStatus: http.StatusNotFound, wantError: "tasks not found"},
		{name: "object body", method: http.MethodPost, body: `{"ids":["task-a"]}`, wantStatus: http.StatusBadRequest, wantError: "JSON array"},
		{name: "non string item", method: http.MethodPost, body: `["task-a", 1]`, wantStatus: http.StatusBadRequest, wantError: "JSON array"},
		{name: "malformed json", method: http.MethodPost, body: `[`, wantStatus: http.StatusBadRequest, wantError: "JSON array"},
		{name: "multiple json values", method: http.MethodPost, body: `[] []`, wantStatus: http.StatusBadRequest, wantError: "single JSON array"},
		{name: "maximum ids accepted", method: http.MethodPost, body: string(maximumBody), wantStatus: http.StatusNotFound, wantError: "tasks not found"},
		{name: "too many ids", method: http.MethodPost, body: string(tooManyBody), wantStatus: http.StatusBadRequest, wantError: "maximum is 1000"},
		{name: "body too large", method: http.MethodPost, body: oversizedBody, wantStatus: http.StatusBadRequest, wantError: "JSON array"},
		{name: "get is not supported", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, publicTaskDetailsPath, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantError != "" && !strings.Contains(response.Body.String(), test.wantError) {
				t.Fatalf("body = %q, want error containing %q", response.Body.String(), test.wantError)
			}
		})
	}
}

func TestGetTasksByTaskIDsSupportsMoreThanOneSQLiteChunk(t *testing.T) {
	_, store := newBatchTaskTestServer(t)
	requested := make([]string, 0, 501)
	for index := 0; index < 501; index++ {
		requested = append(requested, fmt.Sprintf("missing-%03d", index))
	}
	createBatchTaskFixture(t, store, requested[0], StatusPending, nil)
	createBatchTaskFixture(t, store, requested[len(requested)-1], StatusPending, nil)

	tasks, err := store.GetTasksByTaskIDs(context.Background(), requested)
	if err != nil {
		t.Fatalf("GetTasksByTaskIDs: %v", err)
	}
	if len(tasks) != 2 || tasks[0].TaskID != requested[0] || tasks[1].TaskID != requested[len(requested)-1] {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func newBatchTaskTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg)), store
}

func createBatchTaskFixture(t *testing.T, store *Store, taskID string, status int, final json.RawMessage) {
	t.Helper()
	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg",
		SceneName:  "gay_cumshot_10eros",
		TaskID:     taskID,
		Fee:        "10",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal task request: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), taskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	switch status {
	case StatusPending:
		return
	case StatusRunning:
		err = store.MarkTaskRunning(context.Background(), task.ID, StepAnimeVideo)
	case StatusFailed:
		err = store.MarkTaskFailed(context.Background(), task.ID, "backend failed")
	case StatusSuccess:
		err = store.MarkTaskSuccess(context.Background(), task.ID, final)
	default:
		t.Fatalf("unsupported fixture status %d", status)
	}
	if err != nil {
		t.Fatalf("update task status: %v", err)
	}
}
