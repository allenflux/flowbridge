package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkflowSendsTerminalCallbackWithParentID(t *testing.T) {
	const taskID = "bridge-callback"
	const notifyURL = "https://callback.example/result?token=test"
	const output = `[{"download_url":"https://cdn.example/final.mp4"}]`

	for _, test := range []struct {
		name   string
		status int
		extra  string
	}{
		{name: "success", status: StatusSuccess, extra: `,"out_data":` + output},
		{name: "failure", status: StatusFailed, extra: `,"error":"video generation failed"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.WorkerConcurrency = 1
			cfg.CallbackConcurrency = 1
			cfg.PollInterval = time.Millisecond
			store, err := OpenStore(":memory:")
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			defer store.Close()

			recorder := &backendRecorder{}
			videoPolling := make(chan struct{})
			finishVideo := make(chan struct{})
			backend := NewBackendClient(cfg)
			backend.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet && req.URL.Query().Get("task_id") == "video-task" {
					if test.status == StatusSuccess {
						close(videoPolling)
						select {
						case <-finishVideo:
						case <-req.Context().Done():
							return nil, req.Context().Err()
						}
					}
					body := fmt.Sprintf(`{"task_id":"bridge-callback_video","uuid":"p0-video-uuid","status":%d%s}`, test.status, test.extra)
					return testJSONResponse(req, http.StatusOK, body), nil
				}
				return recorder.roundTrip(req)
			})
			worker := NewWorker(store, backend, cfg)
			payloads := make(chan map[string]json.RawMessage, 1)
			var attempts atomic.Int32
			worker.callbacks.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				if attempts.Add(1) > 1 {
					t.Error("workflow callback was sent more than once")
					return testJSONResponse(req, http.StatusOK, `{}`), nil
				}
				if req.Method != http.MethodPost || req.URL.String() != notifyURL || req.Header.Get("Content-Type") != "application/json" {
					t.Errorf("callback request = %s %s (%s)", req.Method, req.URL, req.Header.Get("Content-Type"))
				}
				for _, name := range []string{"Apikey", "X-API-Key", "Authorization", "Cookie", "X-Workflow-Trace"} {
					if len(req.Header.Values(name)) != 0 {
						t.Errorf("callback included upstream header %s", name)
					}
				}
				persisted, err := store.GetPublicTask(req.Context(), taskID)
				if err != nil || persisted.Status != test.status || persisted.FinishedAt == nil {
					t.Errorf("task was not terminal at time of callback: %+v, %v", persisted, err)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Errorf("decode callback body: %v", err)
				}
				payloads <- body
				return testJSONResponse(req, http.StatusOK, `{}`), nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			worker.Start(ctx)
			defer func() {
				cancel()
				waitCtx, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancelWait()
				if err := worker.Wait(waitCtx); err != nil {
					t.Errorf("wait for worker: %v", err)
				}
			}()

			form := url.Values{
				"task_id":     {taskID},
				"source_path": {"https://input.example/source.jpg"},
				"scene_name":  {"gay_cumshot_10eros"},
				"notify_url":  {notifyURL},
			}
			req := httptest.NewRequest(http.MethodPost, publicTenErosImageToVideoPath, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Apikey", "p0-api-key")
			req.Header.Set("X-API-Key", "p0-api-key")
			req.Header.Set("Authorization", "Bearer p0-token")
			req.Header.Set("Cookie", "session=p0-session")
			req.Header.Set("X-Workflow-Trace", "p0-trace")
			response := httptest.NewRecorder()
			NewServer(cfg, store, worker).ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("create workflow returned %d: %s", response.Code, response.Body.String())
			}
			var created PublicTaskResponse
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.TaskID != taskID {
				t.Fatalf("create workflow task_id = %q, want %q: %v", created.TaskID, taskID, err)
			}
			if test.status == StatusSuccess {
				select {
				case <-videoPolling:
				case <-time.After(5 * time.Second):
					t.Fatal("video step did not start polling after the image completed")
				}
				select {
				case <-payloads:
					t.Fatal("workflow callback arrived while the video step was still running")
				case <-time.After(25 * time.Millisecond):
				}
				close(finishVideo)
			}

			select {
			case body := <-payloads:
				for _, name := range []string{"task_id", "uuid"} {
					if got := string(body[name]); got != fmt.Sprintf("%q", created.TaskID) {
						t.Errorf("callback %s = %s, want %q", name, got, created.TaskID)
					}
				}
				if got := string(body["status"]); got != fmt.Sprint(test.status) {
					t.Errorf("callback status = %s, want %d", got, test.status)
				}
				if test.status == StatusSuccess && string(body["out_data"]) != output {
					t.Errorf("callback out_data = %s, want %s", body["out_data"], output)
				}
				if test.status == StatusFailed && len(body["error"]) == 0 {
					t.Error("failure callback did not contain an error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for a workflow callback")
			}
			requests := recorder.snapshot()
			if len(requests) != 2 {
				t.Fatalf("P0 submissions = %d, want 2", len(requests))
			}
			for _, request := range requests {
				if _, present := request.Form["notify_url"]; present {
					t.Errorf("P0 request for %s included notify_url", request.Path)
				}
			}
		})
	}
}
