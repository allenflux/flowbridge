package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignP0CallbackMatchesBackend(t *testing.T) {
	// Sorted JSON with a signature computed by P0's src/mq/gateway.py.
	body := []byte(`{"error_code":"SUCCESS","hash_key":"test_secret","is_encrypt":true,"notify_type":null,"out_data":[{"download_url":"https://media.example/a'b","encryption_key":"k"}],"status":2,"task_id":"bridge_test","title":"","uuid":"bridge_test","zz_empty":""}`)
	const want = "aeb5acbd5556638985e5b43e86250b7c"
	sign, err := signP0Callback(body)
	if err != nil || sign != want {
		t.Fatalf("signature = %q, %v, want %s", sign, err, want)
	}
}

func TestBuildCallbackPayloadKeepsEncryptedResultAndReplacesIdentity(t *testing.T) {
	createdAt := time.Date(2026, 11, 18, 8, 0, 0, 123456789, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	task := WorkflowTask{
		TaskID: "overall", WorkflowType: WorkflowTenErosImageVideo, Status: StatusSuccess,
		RequestPayload: json.RawMessage(`{"task_id":"caller","apikey":"sensitive","notify_url":"https://callback.example/done","backend_headers":{"Authorization":["sensitive"]}}`),
		FinalResult: json.RawMessage(`{
			"task_id":"overall_video","uuid":"p0-step","bid":"overall_video","status":2,
			"task_type":"generate_videos_scenes_ltx_8s","source_path":"https://media.example/processed.png",
			"hash_key":"p0-hash","is_encrypt":true,"notify_type":"encrypt",
			"out_data":[{"encryption_key":"media-key","download_url":"https://media.example/video"}],
			"sign":"old","api_key":"sensitive","Authorization":"sensitive","notify_status":"old",
			"meta":{"internal":true},"messages":{"internal":true},"created_at":"p0","updated_at":"p0"
		}`),
		CreatedAt: createdAt, UpdatedAt: updatedAt, FinishedAt: &updatedAt,
	}
	body, err := buildCallbackPayload(task, "https://callback.example/done", nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	wantFields := map[string]string{
		"task_id": `"overall"`, "uuid": `"overall"`, "bid": `"overall"`,
		"status": `2`, "error_code": `"SUCCESS"`, "hash_key": `"p0-hash"`,
		"notify_url": `"https://callback.example/done"`, "notify_type": `"encrypt"`,
		"created_at": `"2026-11-18T08:00:00.123456"`, "updated_at": `"2026-11-18T08:00:01.123456"`,
		"out_data": `[{"encryption_key":"media-key","download_url":"https://media.example/video"}]`,
	}
	for field, want := range wantFields {
		if got := string(payload[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
	if len(payload["sign"]) != 34 || strings.Contains(string(body), "sensitive") || strings.Contains(string(body), `"meta"`) || strings.Contains(string(body), `"messages"`) || strings.Contains(string(body), `"notify_status"`) {
		t.Fatalf("unexpected callback payload: %s", body)
	}
}

func TestBuildFailedCallbackPayloadUsesOnlyTerminalBackendError(t *testing.T) {
	task := WorkflowTask{
		TaskID: "overall", WorkflowType: WorkflowMinimaxH3ImageVideo, Status: StatusFailed,
		RequestPayload: json.RawMessage(`{"bid":"business-id","hash_key":"client-hash","apikey":"literal + key","scene_name":"character_turnaround_sheet_h3"}`),
		ErrorMessage:   "poll https://p0.example/task?apikey=literal+%2B+key timed out",
	}
	tests := []struct {
		name, result, wantError, wantCode string
	}{
		{"terminal error", `{"status":"-1","error":"model rejected input","error_code":"INVALID_PARAMS","bid":"segment-bid","out_data":["segment-result"]}`, "model rejected input", "INVALID_PARAMS"},
		{"poll error", `{"status":1,"error":"interim error","error_code":"ERROR"}`, "poll https://p0.example/task?apikey=**** timed out", "FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := buildCallbackPayload(task, "https://callback.example/done", json.RawMessage(test.result))
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				TaskID    string        `json:"task_id"`
				Status    int           `json:"status"`
				BID       string        `json:"bid"`
				Error     string        `json:"error"`
				ErrorCode string        `json:"error_code"`
				OutData   []interface{} `json:"out_data"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.TaskID != task.TaskID || payload.Status != StatusFailed || payload.BID != "business-id" || payload.Error != test.wantError || payload.ErrorCode != test.wantCode || payload.OutData == nil || len(payload.OutData) != 0 {
				t.Fatalf("unexpected failure payload: %s", body)
			}
		})
	}
}
