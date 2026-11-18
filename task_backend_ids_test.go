package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPublicTaskBackendIDs(t *testing.T) {
	server, store := newBatchTaskTestServer(t)
	ctx := context.Background()
	taskID := "bridge_backend_ids"
	req := AnimeVideoRequest{TaskID: taskID}
	task, err := store.CreateAnimeVideoTaskForWorkflow(ctx, taskID, WorkflowTenErosBatch2ImageVideo, req,
		json.RawMessage(`{"task_id":"bridge_backend_ids","apikey":"private-key"}`))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	target := "/api/public/task/backend-ids?task_id=" + taskID

	assertResponse := func(wantSteps string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
		}
		var got, want any
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		wantJSON := `{"task_id":"bridge_backend_ids","workflow_type":"10eros_batch2_image_to_video","steps":` + wantSteps + `}`
		if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
			t.Fatalf("decode expected response: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("response = %s, want %s", response.Body.String(), wantJSON)
		}
	}

	assertResponse(`[
		{"step_name":"anime_image","task_id":null,"uuid":null},
		{"step_name":"anime_video","task_id":null,"uuid":null}
	]`)

	if err := store.UpdateStepAccepted(ctx, task.ID, 1, "image-uuid", json.RawMessage(`{"uuid":"image-uuid"}`)); err != nil {
		t.Fatalf("accept image step: %v", err)
	}
	assertResponse(`[
		{"step_name":"anime_image","task_id":null,"uuid":"image-uuid"},
		{"step_name":"anime_video","task_id":null,"uuid":null}
	]`)

	if err := store.MarkStepSuccess(ctx, task.ID, 1, json.RawMessage(`{"task_id":"image-id","uuid":"other-image-uuid","status":2}`)); err != nil {
		t.Fatalf("complete image step: %v", err)
	}
	if err := store.UpdateStepAccepted(ctx, task.ID, 2, "video-id", json.RawMessage(`{"task_id":"video-id","extra":"private-value"}`)); err != nil {
		t.Fatalf("accept video step: %v", err)
	}
	if err := store.MarkStepSuccess(ctx, task.ID, 2, json.RawMessage(`{"task_id":"other-video-id","uuid":"video-uuid","status":2}`)); err != nil {
		t.Fatalf("complete video step: %v", err)
	}
	assertResponse(`[
		{"step_name":"anime_image","task_id":"image-id","uuid":"image-uuid"},
		{"step_name":"anime_video","task_id":"video-id","uuid":"video-uuid"}
	]`)
}

func TestPublicTaskBackendIDsErrors(t *testing.T) {
	server, _ := newBatchTaskTestServer(t)
	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "missing ID", wantStatus: http.StatusBadRequest},
		{name: "blank ID", query: "?task_id=%20", wantStatus: http.StatusBadRequest},
		{name: "unknown ID", query: "?task_id=missing", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/public/task/backend-ids"+test.query, nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}
