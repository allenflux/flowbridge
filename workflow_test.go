package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedBackendRequest struct {
	Path   string
	Form   url.Values
	APIKey string
}

type backendRecorder struct {
	mu       sync.Mutex
	requests []capturedBackendRequest
}

func (r *backendRecorder) roundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && req.URL.Path == "/api/public/task" {
		taskID := req.URL.Query().Get("task_id")
		if strings.HasPrefix(taskID, "image-") || strings.HasSuffix(taskID, "_image") {
			return testJSONResponse(req, http.StatusOK, fmt.Sprintf(`{"task_id":%q,"status":2,"out_data":[{"download_url":"https://cdn.example/intermediate.jpg"}]}`, taskID)), nil
		}
		return testJSONResponse(req, http.StatusOK, fmt.Sprintf(`{"task_id":%q,"status":2,"out_data":["https://cdn.example/final.mp4"]}`, taskID)), nil
	}

	if req.Method != http.MethodPost {
		return testJSONResponse(req, http.StatusNotFound, `{"error":"unexpected request"}`), nil
	}
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.requests = append(r.requests, capturedBackendRequest{
		Path:   req.URL.Path,
		Form:   cloneValues(req.Form),
		APIKey: req.Header.Get("Apikey"),
	})
	r.mu.Unlock()

	if req.URL.Path == backendUndressAnimeImagePath || req.URL.Path == backendQwenTwoImagePath {
		return testJSONResponse(req, http.StatusOK, `{"task_id":"image-task"}`), nil
	}
	return testJSONResponse(req, http.StatusOK, `{"task_id":"video-task"}`), nil
}

func testJSONResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func (r *backendRecorder) snapshot() []capturedBackendRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]capturedBackendRequest, len(r.requests))
	copy(result, r.requests)
	return result
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func TestTenErosWorkflowsRouteAndForwardParameters(t *testing.T) {
	audioOff := false
	watermarkOn := true
	tests := []struct {
		name              string
		scene             string
		videoScene        string
		targetPath        string
		expectedImagePath string
		expectedVideoPath string
	}{
		{name: "gay doggy", scene: "gay_doggy_10eros", targetPath: "https://input.example/doggy-target.jpg", expectedImagePath: backendQwenTwoImagePath, expectedVideoPath: backendLTX8sVideoPath},
		{name: "gay cumshot", scene: "gay_cumshot_10eros", expectedImagePath: backendUndressAnimeImagePath, expectedVideoPath: backendLTX8sVideoPath},
		{name: "gay anal creampie", scene: "gay_anal_creampie_10eros", expectedImagePath: backendUndressAnimeImagePath, expectedVideoPath: backendLTX8sVideoPath},
		{name: "lesbian kiss", scene: "lesbian_kiss_10eros", targetPath: "https://input.example/target.jpg", expectedImagePath: backendQwenTwoImagePath, expectedVideoPath: backendLTX8sVideoPath},
		{name: "legacy", scene: "goal_kick_portugal", videoScene: "disney_real_anime_greet", expectedImagePath: backendUndressAnimeImagePath, expectedVideoPath: backendUndressAnimeVideoPath},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &backendRecorder{}

			cfg := defaultConfig()
			cfg.BackendBaseURL = "http://backend.example"
			cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
			cfg.PollInterval = time.Millisecond
			cfg.TaskTimeout = 2 * time.Second
			cfg.HTTPTimeout = time.Second
			cfg.MaxSubmitRetries = 0

			store, err := OpenStore(cfg.DBPath)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			defer store.Close()

			req := AnimeVideoRequest{
				SourcePath:         "https://input.example/source.jpg",
				TargetPath:         test.targetPath,
				SceneName:          test.scene,
				VideoSceneName:     test.videoScene,
				IncomingPrompt:     "shared prompt",
				QwenIncomingPrompt: "image prompt",
				WanIncomingPrompt:  "video prompt",
				OutputFormat:       "video",
				VideoFormat:        "video/h265-mp4",
				AudioEnabled:       &audioOff,
				IsWatermark:        &watermarkOn,
				IsEncrypt:          true,
				BID:                "bid-1",
				AppID:              "app-1",
				Fee:                "12",
				Title:              "title-1",
				HashKey:            "hash-1",
				APIKey:             "api-key-1",
				NotifyURL:          "https://callback.example/done",
				TaskID:             "bridge-1",
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
			if err != nil {
				t.Fatalf("CreateAnimeVideoTask: %v", err)
			}

			backend := NewBackendClient(cfg)
			backend.client.Transport = roundTripFunc(recorder.roundTrip)
			worker := NewWorker(store, backend, cfg)
			if err := worker.runTask(context.Background(), task.ID); err != nil {
				t.Fatalf("runTask: %v", err)
			}

			requests := recorder.snapshot()
			if len(requests) != 2 {
				t.Fatalf("backend POST count = %d, want 2: %#v", len(requests), requests)
			}
			if requests[0].Path != test.expectedImagePath {
				t.Fatalf("image path = %q, want %q", requests[0].Path, test.expectedImagePath)
			}
			if requests[1].Path != test.expectedVideoPath {
				t.Fatalf("video path = %q, want %q", requests[1].Path, test.expectedVideoPath)
			}
			for index, request := range requests {
				if request.APIKey != req.APIKey {
					t.Errorf("request %d apikey = %q, want %q", index, request.APIKey, req.APIKey)
				}
			}

			expectedImageForm := url.Values{
				"source_path":     {req.SourcePath},
				"scene_name":      {test.scene},
				"incoming_prompt": {req.QwenIncomingPrompt},
				"bid":             {req.BID},
				"app_id":          {req.AppID},
				"fee":             {req.Fee},
				"title":           {req.Title},
				"is_encrypt":      {"false"},
				"is_watermark":    {"false"},
				"task_id":         {"bridge-1_image"},
			}
			if test.expectedImagePath == backendQwenTwoImagePath {
				expectedImageForm.Set("target_path", req.TargetPath)
			} else {
				expectedImageForm.Set("hash_key", req.HashKey)
			}
			if test.expectedVideoPath == backendUndressAnimeVideoPath {
				expectedImageForm.Set("notify_url", req.NotifyURL)
			}
			assertValuesEqual(t, requests[0].Form, expectedImageForm)

			expectedVideoScene := test.videoScene
			if expectedVideoScene == "" {
				expectedVideoScene = test.scene
			}
			expectedVideoForm := url.Values{
				"source_path":  {"https://cdn.example/intermediate.jpg"},
				"scene_name":   {expectedVideoScene},
				"bid":          {req.BID},
				"app_id":       {req.AppID},
				"fee":          {req.Fee},
				"title":        {req.Title},
				"hash_key":     {req.HashKey},
				"is_encrypt":   {"true"},
				"is_watermark": {"true"},
				"video_format": {req.VideoFormat},
				"task_id":      {"bridge-1_video"},
			}
			if test.expectedVideoPath == backendLTX8sVideoPath {
				expectedVideoForm.Set("incoming_prompt", req.WanIncomingPrompt)
				expectedVideoForm.Set("audio_enabled", "false")
				expectedVideoForm.Set("notify_url", req.NotifyURL)
			} else {
				expectedVideoForm.Set("qwen_incoming_prompt", req.QwenIncomingPrompt)
				expectedVideoForm.Set("wan_incoming_prompt", req.WanIncomingPrompt)
				expectedVideoForm.Set("output_format", req.OutputFormat)
			}
			assertValuesEqual(t, requests[1].Form, expectedVideoForm)

			detail, err := store.GetTaskDetail(context.Background(), req.TaskID)
			if err != nil {
				t.Fatalf("GetTaskDetail: %v", err)
			}
			if detail.Status != StatusSuccess {
				t.Fatalf("workflow status = %d, want %d", detail.Status, StatusSuccess)
			}
		})
	}
}

func TestMinimaxH3WorkflowRoutesAndForwardsParameters(t *testing.T) {
	recorder := &backendRecorder{}
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.PollInterval = time.Millisecond
	cfg.TaskTimeout = 2 * time.Second
	cfg.HTTPTimeout = time.Second
	cfg.MaxSubmitRetries = 0

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	req := AnimeVideoRequest{
		SourcePath:         "https://input.example/source.jpg",
		SceneName:          minimaxH3CharacterTurnaroundScene,
		VideoSceneName:     minimaxH3CharacterTurnaroundScene,
		IncomingPrompt:     "shared prompt",
		QwenIncomingPrompt: "image prompt",
		BID:                "bid-h3",
		AppID:              "app-h3",
		Fee:                "12",
		Title:              "title-h3",
		HashKey:            "hash-h3",
		APIKey:             "api-key-h3",
		NotifyURL:          "https://callback.example/h3",
		TaskID:             "bridge-h3",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	if task.WorkflowType != WorkflowMinimaxH3ImageVideo {
		t.Fatalf("workflow type = %q, want %q", task.WorkflowType, WorkflowMinimaxH3ImageVideo)
	}

	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(recorder.roundTrip)
	worker := NewWorker(store, backend, cfg)
	if err := worker.runTask(context.Background(), task.ID); err != nil {
		t.Fatalf("runTask: %v", err)
	}

	requests := recorder.snapshot()
	if len(requests) != 2 {
		t.Fatalf("backend POST count = %d, want 2: %#v", len(requests), requests)
	}
	if requests[0].Path != backendUndressAnimeImagePath {
		t.Fatalf("image path = %q, want %q", requests[0].Path, backendUndressAnimeImagePath)
	}
	if requests[1].Path != backendMinimaxH3MultiPath {
		t.Fatalf("video path = %q, want %q", requests[1].Path, backendMinimaxH3MultiPath)
	}
	for index, request := range requests {
		if request.APIKey != req.APIKey {
			t.Errorf("request %d apikey = %q, want %q", index, request.APIKey, req.APIKey)
		}
		if request.Form.Get("scene_name") != minimaxH3CharacterTurnaroundScene {
			t.Errorf("request %d scene_name = %q", index, request.Form.Get("scene_name"))
		}
	}

	expectedImageForm := url.Values{
		"source_path":     {req.SourcePath},
		"scene_name":      {minimaxH3CharacterTurnaroundScene},
		"incoming_prompt": {req.QwenIncomingPrompt},
		"bid":             {req.BID},
		"app_id":          {req.AppID},
		"fee":             {req.Fee},
		"title":           {req.Title},
		"hash_key":        {req.HashKey},
		"is_encrypt":      {"false"},
		"is_watermark":    {"false"},
		"task_id":         {"bridge-h3_image"},
	}
	assertValuesEqual(t, requests[0].Form, expectedImageForm)

	expectedVideoForm := url.Values{
		"source_path": {"https://cdn.example/intermediate.jpg"},
		"scene_name":  {minimaxH3CharacterTurnaroundScene},
		"bid":         {req.BID},
		"app_id":      {req.AppID},
		"fee":         {req.Fee},
		"notify_url":  {req.NotifyURL},
		"task_id":     {"bridge-h3_video"},
	}
	assertValuesEqual(t, requests[1].Form, expectedVideoForm)

	detail, err := store.GetTaskDetail(context.Background(), req.TaskID)
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	if detail.Status != StatusSuccess {
		t.Fatalf("workflow status = %d, want %d", detail.Status, StatusSuccess)
	}
}

func TestMinimaxH3PublicRouteIsolatedAndValidated(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))

	validForm := url.Values{
		"source_path": {"https://input.example/source.jpg"},
		"scene_name":  {minimaxH3CharacterTurnaroundScene},
		"task_id":     {"accept-minimax-h3"},
	}
	validRequest := httptest.NewRequest(http.MethodPost, publicMinimaxH3MultiPath, strings.NewReader(validForm.Encode()))
	validRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRequest.Header.Set("Apikey", "api-key-h3")
	validResponse := httptest.NewRecorder()
	server.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid status = %d, want 200: %s", validResponse.Code, validResponse.Body.String())
	}
	var public PublicTaskResponse
	if err := json.Unmarshal(validResponse.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if public.TaskType != WorkflowMinimaxH3ImageVideo || public.SceneName != minimaxH3CharacterTurnaroundScene {
		t.Fatalf("task type/scene = %q/%q", public.TaskType, public.SceneName)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		form       url.Values
		apiKey     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "missing api key",
			method:     http.MethodPost,
			path:       publicMinimaxH3MultiPath,
			form:       url.Values{"source_path": {"https://input.example/source.jpg"}, "scene_name": {minimaxH3CharacterTurnaroundScene}},
			wantStatus: http.StatusBadRequest,
			wantBody:   "Apikey is required",
		},
		{
			name:       "unsupported scene",
			method:     http.MethodPost,
			path:       publicMinimaxH3MultiPath,
			form:       url.Values{"source_path": {"https://input.example/source.jpg"}, "scene_name": {"goal_kick_portugal"}},
			apiKey:     "api-key-h3",
			wantStatus: http.StatusBadRequest,
			wantBody:   minimaxH3CharacterTurnaroundScene,
		},
		{
			name:       "different video scene",
			method:     http.MethodPost,
			path:       publicMinimaxH3MultiPath,
			form:       url.Values{"source_path": {"https://input.example/source.jpg"}, "scene_name": {minimaxH3CharacterTurnaroundScene}, "video_scene_name": {"other"}},
			apiKey:     "api-key-h3",
			wantStatus: http.StatusBadRequest,
			wantBody:   "must equal scene_name",
		},
		{
			name:       "h3 scene on legacy route",
			method:     http.MethodPost,
			path:       "/api/public/generate/undress/anime/video",
			form:       url.Values{"source_path": {"https://input.example/source.jpg"}, "scene_name": {minimaxH3CharacterTurnaroundScene}},
			apiKey:     "api-key-h3",
			wantStatus: http.StatusBadRequest,
			wantBody:   publicMinimaxH3MultiPath,
		},
		{
			name:       "get is not supported",
			method:     http.MethodGet,
			path:       publicMinimaxH3MultiPath,
			apiKey:     "api-key-h3",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.apiKey != "" {
				request.Header.Set("Apikey", test.apiKey)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus || (test.wantBody != "" && !strings.Contains(response.Body.String(), test.wantBody)) {
				t.Fatalf("status/body = %d %q, want %d containing %q", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestLegacyWorkflowKeepsOriginalEndpoints(t *testing.T) {
	req := AnimeVideoRequest{
		SourcePath:         "https://input.example/source.jpg",
		SceneName:          "goal_kick_portugal",
		VideoSceneName:     "disney_real_anime_greet",
		QwenIncomingPrompt: "image prompt",
		WanIncomingPrompt:  "video prompt",
		OutputFormat:       "video",
	}
	spec, videoScene, err := resolveBackendWorkflow(req)
	if err != nil {
		t.Fatalf("resolveBackendWorkflow: %v", err)
	}
	if spec.ImagePath != backendUndressAnimeImagePath || spec.VideoPath != backendUndressAnimeVideoPath {
		t.Fatalf("legacy endpoints = %#v", spec)
	}
	if videoScene != req.VideoSceneName {
		t.Fatalf("legacy video scene = %q, want %q", videoScene, req.VideoSceneName)
	}

	videoForm := buildBackendVideoForm(req, spec, videoScene, "https://cdn.example/intermediate.jpg")
	if videoForm["qwen_incoming_prompt"] != req.QwenIncomingPrompt || videoForm["wan_incoming_prompt"] != req.WanIncomingPrompt {
		t.Fatalf("legacy prompt fields not preserved: %#v", videoForm)
	}
	if videoForm["output_format"] != "video" {
		t.Fatalf("legacy output_format = %q", videoForm["output_format"])
	}
}

func TestRunningStepRecoversAcceptedSubmissionWithoutDuplicatePost(t *testing.T) {
	recorder := &backendRecorder{}
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.PollInterval = time.Millisecond
	cfg.TaskTimeout = 2 * time.Second
	cfg.HTTPTimeout = time.Second
	cfg.MaxSubmitRetries = 0

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	req := AnimeVideoRequest{
		SourcePath:     "https://input.example/source.jpg",
		TargetPath:     "https://input.example/target.jpg",
		SceneName:      "gay_doggy_10eros",
		VideoSceneName: "gay_doggy_10eros",
		VideoFormat:    "video/h264-mp4",
		APIKey:         "api-key-1",
		TaskID:         "bridge-recovery",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	spec, _, err := resolveBackendWorkflow(req)
	if err != nil {
		t.Fatalf("resolveBackendWorkflow: %v", err)
	}
	imageFormRaw, err := json.Marshal(buildBackendImageForm(req, spec))
	if err != nil {
		t.Fatalf("marshal image form: %v", err)
	}
	if err := store.UpdateStepStart(context.Background(), task.ID, 1, imageFormRaw); err != nil {
		t.Fatalf("UpdateStepStart: %v", err)
	}

	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(recorder.roundTrip)
	worker := NewWorker(store, backend, cfg)
	if err := worker.runTask(context.Background(), task.ID); err != nil {
		t.Fatalf("runTask: %v", err)
	}

	requests := recorder.snapshot()
	if len(requests) != 1 || requests[0].Path != backendLTX8sVideoPath {
		t.Fatalf("POST requests = %#v, want only the video submission", requests)
	}
	if requests[0].Form.Get("source_path") != "https://cdn.example/intermediate.jpg" {
		t.Fatalf("video source_path = %q", requests[0].Form.Get("source_path"))
	}
}

func TestAmbiguousFinalSubmitRecoversAcceptedTask(t *testing.T) {
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.PollInterval = time.Millisecond
	cfg.TaskTimeout = 2 * time.Second
	cfg.HTTPTimeout = time.Second
	cfg.MaxSubmitRetries = 0

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg",
		TargetPath: "https://input.example/target.jpg",
		SceneName:  "gay_doggy_10eros",
		APIKey:     "api-key-1",
		TaskID:     "bridge-ambiguous-accepted",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}

	var mu sync.Mutex
	imagePosts := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == backendQwenTwoImagePath:
			mu.Lock()
			imagePosts++
			mu.Unlock()
			return testJSONResponse(request, http.StatusGatewayTimeout, `{"error":"upstream timed out after accepting"}`), nil
		case request.Method == http.MethodPost && request.URL.Path == backendLTX8sVideoPath:
			return testJSONResponse(request, http.StatusOK, `{"task_id":"video-task"}`), nil
		case request.Method == http.MethodGet && request.URL.Path == "/api/public/task":
			taskID := request.URL.Query().Get("task_id")
			if taskID == "bridge-ambiguous-accepted_image" {
				return testJSONResponse(request, http.StatusOK, `{"task_id":"bridge-ambiguous-accepted_image","status":2,"out_data":["https://cdn.example/intermediate.jpg"]}`), nil
			}
			return testJSONResponse(request, http.StatusOK, `{"task_id":"video-task","status":2,"out_data":["https://cdn.example/final.mp4"]}`), nil
		default:
			return testJSONResponse(request, http.StatusNotFound, `{"error":"unexpected request"}`), nil
		}
	})

	worker := NewWorker(store, backend, cfg)
	if err := worker.runTask(context.Background(), task.ID); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	mu.Lock()
	gotImagePosts := imagePosts
	mu.Unlock()
	if gotImagePosts != 1 {
		t.Fatalf("image POST count = %d, want 1", gotImagePosts)
	}
	detail, err := store.GetTaskDetail(context.Background(), req.TaskID)
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	if detail.Status != StatusSuccess {
		t.Fatalf("workflow status = %d, want %d", detail.Status, StatusSuccess)
	}
}

func TestAmbiguousSubmitWithIndeterminateLookupStaysRunnable(t *testing.T) {
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.PollInterval = time.Millisecond
	cfg.TaskTimeout = 2 * time.Second
	cfg.HTTPTimeout = time.Second
	cfg.MaxSubmitRetries = 0

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg",
		TargetPath: "https://input.example/target.jpg",
		SceneName:  "gay_doggy_10eros",
		APIKey:     "api-key-1",
		TaskID:     "bridge-ambiguous-indeterminate",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}

	var mu sync.Mutex
	imagePosts := 0
	taskQueries := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == backendQwenTwoImagePath:
			mu.Lock()
			imagePosts++
			mu.Unlock()
			return testJSONResponse(request, http.StatusBadGateway, `{"error":"ambiguous gateway response"}`), nil
		case request.Method == http.MethodGet && request.URL.Path == "/api/public/task":
			mu.Lock()
			taskQueries++
			queryNumber := taskQueries
			mu.Unlock()
			if queryNumber == 1 {
				return testJSONResponse(request, http.StatusServiceUnavailable, `{"error":"task lookup unavailable"}`), nil
			}
			return testJSONResponse(request, http.StatusNotFound, `{"error":"task not found"}`), nil
		default:
			return testJSONResponse(request, http.StatusNotFound, `{"error":"unexpected request"}`), nil
		}
	})

	worker := NewWorker(store, backend, cfg)
	worker.submissionRecoveryWindow = 2 * time.Millisecond
	err = worker.runTask(context.Background(), task.ID)
	if !errors.Is(err, errBackendSubmissionUncertain) {
		t.Fatalf("runTask error = %v, want errBackendSubmissionUncertain", err)
	}
	mu.Lock()
	gotImagePosts := imagePosts
	gotTaskQueries := taskQueries
	mu.Unlock()
	if gotImagePosts != 1 {
		t.Fatalf("image POST count = %d, want 1", gotImagePosts)
	}
	if gotTaskQueries != 2 {
		t.Fatalf("task query count = %d, want 2", gotTaskQueries)
	}

	detail, err := store.GetTaskDetail(context.Background(), req.TaskID)
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	if detail.Status != StatusRunning {
		t.Fatalf("workflow status = %d, want %d", detail.Status, StatusRunning)
	}
	if detail.Steps[0].Status != StatusRunning {
		t.Fatalf("image step status = %d, want %d", detail.Steps[0].Status, StatusRunning)
	}
}

func TestUnknownSubmitOutcomeChecksAcceptance(t *testing.T) {
	tests := []struct {
		name       string
		postResult func(*http.Request) (*http.Response, error)
	}{
		{
			name: "network error after request write",
			postResult: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset after request write")
			},
		},
		{
			name: "successful response with invalid JSON",
			postResult: func(request *http.Request) (*http.Response, error) {
				return testJSONResponse(request, http.StatusOK, `accepted but response was truncated`), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.BackendBaseURL = "http://backend.example"
			cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
			cfg.HTTPTimeout = time.Second
			cfg.MaxSubmitRetries = 0

			store, err := OpenStore(cfg.DBPath)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			defer store.Close()

			postCount := 0
			backend := NewBackendClient(cfg)
			backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPost {
					postCount++
					return test.postResult(request)
				}
				if request.Method == http.MethodGet && request.URL.Path == "/api/public/task" {
					return testJSONResponse(request, http.StatusOK, `{"task_id":"bridge-unknown_image","status":1}`), nil
				}
				return testJSONResponse(request, http.StatusNotFound, `{"error":"unexpected request"}`), nil
			})

			worker := NewWorker(store, backend, cfg)
			_, response, err := worker.postBackendForm(
				context.Background(),
				999,
				1,
				backendUndressAnimeImagePath,
				map[string]string{"task_id": "bridge-unknown_image"},
				"api-key-1",
			)
			if err != nil {
				t.Fatalf("postBackendForm: %v", err)
			}
			if postCount != 1 {
				t.Fatalf("POST count = %d, want 1", postCount)
			}
			if backendTaskID(response) != "bridge-unknown_image" {
				t.Fatalf("accepted task_id = %q", backendTaskID(response))
			}
		})
	}
}

func TestAmbiguousSubmitRetriesOnlyAfterTwoNotFoundChecks(t *testing.T) {
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.HTTPTimeout = time.Second
	cfg.MaxSubmitRetries = 1

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	postCount := 0
	queryCount := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			postCount++
			if postCount == 1 {
				return testJSONResponse(request, http.StatusBadGateway, `{"error":"ambiguous gateway response"}`), nil
			}
			return testJSONResponse(request, http.StatusOK, `{"task_id":"bridge-retry_image"}`), nil
		}
		if request.Method == http.MethodGet && request.URL.Path == "/api/public/task" {
			queryCount++
			return testJSONResponse(request, http.StatusNotFound, `{"error":"task not found"}`), nil
		}
		return testJSONResponse(request, http.StatusNotFound, `{"error":"unexpected request"}`), nil
	})

	worker := NewWorker(store, backend, cfg)
	worker.submissionRecoveryWindow = time.Millisecond
	_, response, err := worker.postBackendForm(
		context.Background(),
		999,
		1,
		backendUndressAnimeImagePath,
		map[string]string{"task_id": "bridge-retry_image"},
		"api-key-1",
	)
	if err != nil {
		t.Fatalf("postBackendForm: %v", err)
	}
	if postCount != 2 || queryCount != 2 {
		t.Fatalf("POST/query counts = %d/%d, want 2/2", postCount, queryCount)
	}
	if backendTaskID(response) != "bridge-retry_image" {
		t.Fatalf("accepted task_id = %q", backendTaskID(response))
	}
}

func TestTenErosWorkflowValidation(t *testing.T) {
	tests := []struct {
		name string
		req  AnimeVideoRequest
	}{
		{
			name: "lesbian two image scene requires target",
			req:  AnimeVideoRequest{SceneName: "lesbian_kiss_10eros"},
		},
		{
			name: "doggy two image scene requires target",
			req:  AnimeVideoRequest{SceneName: "gay_doggy_10eros"},
		},
		{
			name: "special image scene cannot select a different video scene",
			req: AnimeVideoRequest{
				SceneName:      "gay_doggy_10eros",
				VideoSceneName: "gay_cumshot_10eros",
				TargetPath:     "https://input.example/target.jpg",
			},
		},
		{
			name: "legacy image scene cannot select a special video scene",
			req: AnimeVideoRequest{
				SceneName:      "goal_kick_portugal",
				VideoSceneName: "gay_doggy_10eros",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := resolveBackendWorkflow(test.req); err == nil {
				t.Fatal("resolveBackendWorkflow succeeded, want validation error")
			}
		})
	}
}

func TestParseAnimeVideoRequestPreservesExplicitFalse(t *testing.T) {
	values := url.Values{
		"source_path":   {"https://input.example/source.jpg"},
		"target_path":   {"https://input.example/target.jpg"},
		"scene_name":    {"lesbian_kiss_10eros"},
		"audio_enabled": {"false"},
		"watermark":     {"false"},
		"is_encrypt":    {"true"},
		"video_format":  {"video/h265-mp4"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	parsed, err := parseAnimeVideoRequest(req)
	if err != nil {
		t.Fatalf("parseAnimeVideoRequest: %v", err)
	}
	if parsed.AudioEnabled == nil || *parsed.AudioEnabled {
		t.Fatalf("audio_enabled = %#v, want explicit false", parsed.AudioEnabled)
	}
	if parsed.IsWatermark == nil || *parsed.IsWatermark {
		t.Fatalf("is_watermark = %#v, want explicit false from watermark alias", parsed.IsWatermark)
	}
	if !parsed.IsEncrypt || parsed.VideoFormat != "video/h265-mp4" || parsed.TargetPath != values.Get("target_path") {
		t.Fatalf("extended parameters not preserved: %#v", parsed)
	}
}

func TestParseAnimeVideoJSONSupportsWatermarkAlias(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/",
		strings.NewReader(`{"source_path":"https://input.example/source.jpg","watermark":false}`),
	)
	request.Header.Set("Content-Type", "application/json")

	parsed, err := parseAnimeVideoRequest(request)
	if err != nil {
		t.Fatalf("parseAnimeVideoRequest: %v", err)
	}
	if parsed.IsWatermark == nil || *parsed.IsWatermark {
		t.Fatalf("is_watermark = %#v, want explicit false from JSON watermark alias", parsed.IsWatermark)
	}
}

func TestWatermarkIsDisabledForIntermediateAndAppliedOnlyToFinalStep(t *testing.T) {
	watermarkOff := false
	tests := []struct {
		name               string
		isWatermark        *bool
		expectedFinalValue string
	}{
		{name: "backend-compatible default", expectedFinalValue: "true"},
		{name: "explicitly disabled", isWatermark: &watermarkOff, expectedFinalValue: "false"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := AnimeVideoRequest{
				SourcePath:  "https://input.example/source.jpg",
				TargetPath:  "https://input.example/target.jpg",
				SceneName:   "gay_doggy_10eros",
				IsWatermark: test.isWatermark,
			}
			spec, videoScene, err := resolveBackendWorkflow(req)
			if err != nil {
				t.Fatalf("resolveBackendWorkflow: %v", err)
			}
			imageForm := buildBackendImageForm(req, spec)
			if imageForm["is_watermark"] != "false" {
				t.Fatalf("intermediate is_watermark = %q, want false", imageForm["is_watermark"])
			}
			videoForm := buildBackendVideoForm(req, spec, videoScene, "https://cdn.example/intermediate.jpg")
			if videoForm["is_watermark"] != test.expectedFinalValue {
				t.Fatalf("final is_watermark = %q, want %q", videoForm["is_watermark"], test.expectedFinalValue)
			}
		})
	}
}

func TestWatermarkPostProcessingStatusContinuesPolling(t *testing.T) {
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.PollInterval = time.Millisecond
	cfg.HTTPTimeout = time.Second

	queries := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/public/task" {
			return testJSONResponse(request, http.StatusNotFound, `{"error":"unexpected request"}`), nil
		}
		queries++
		if queries == 1 {
			return testJSONResponse(request, http.StatusOK, `{"task_id":"video-task","status":3}`), nil
		}
		return testJSONResponse(request, http.StatusOK, `{"task_id":"video-task","status":2,"out_data":["https://cdn.example/final.mp4"]}`), nil
	})

	worker := NewWorker(nil, backend, cfg)
	_, result, err := worker.waitBackendTask(context.Background(), 0, 2, "video-task", "api-key-1")
	if err != nil {
		t.Fatalf("waitBackendTask: %v", err)
	}
	if queries != 2 || backendStatus(result) != StatusSuccess {
		t.Fatalf("queries/status = %d/%d, want 2/%d", queries, backendStatus(result), StatusSuccess)
	}
}

func TestCreateAnimeVideoWorkflowAppliesTenErosDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	worker := NewWorker(store, NewBackendClient(cfg), cfg)
	server := NewServer(cfg, store, worker)
	values := url.Values{
		"source_path": {"  https://input.example/source.jpg  "},
		"target_path": {"  https://input.example/target.jpg  "},
		"scene_name":  {"  gay_doggy_10eros  "},
		"task_id":     {"handler-defaults"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		publicTenErosImageToVideoPath,
		strings.NewReader(values.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Apikey", "api-key-1")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var public map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if public["scene_name"] != "gay_doggy_10eros" {
		t.Fatalf("scene_name = %#v", public["scene_name"])
	}
	if public["task_type"] != WorkflowTenErosImageVideo {
		t.Fatalf("task_type = %#v", public["task_type"])
	}
	if public["video_format"] != "video/h264-mp4" {
		t.Fatalf("video_format = %#v", public["video_format"])
	}
	if public["audio_enabled"] != true {
		t.Fatalf("audio_enabled = %#v", public["audio_enabled"])
	}
	if public["is_watermark"] != true {
		t.Fatalf("is_watermark = %#v", public["is_watermark"])
	}
	if value, present := public["is_encrypt"]; !present || value != false {
		t.Fatalf("is_encrypt missing or not false: present=%t value=%#v", present, value)
	}

	detail, err := store.GetTaskDetail(context.Background(), "handler-defaults")
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	var stored AnimeVideoRequest
	if err := json.Unmarshal(detail.RequestPayload, &stored); err != nil {
		t.Fatalf("decode stored request: %v", err)
	}
	if stored.SceneName != "gay_doggy_10eros" || stored.VideoSceneName != stored.SceneName {
		t.Fatalf("stored one-to-one scenes = %q -> %q", stored.SceneName, stored.VideoSceneName)
	}
	if stored.TargetPath != "https://input.example/target.jpg" {
		t.Fatalf("stored target_path = %q", stored.TargetPath)
	}
	if stored.AudioEnabled == nil || !*stored.AudioEnabled || stored.IsWatermark == nil || !*stored.IsWatermark || stored.VideoFormat != "video/h264-mp4" {
		t.Fatalf("stored defaults not applied: %#v", stored)
	}
}

func TestCreateAnimeVideoWorkflowRejectsMissingSecondImage(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	for _, sceneName := range []string{"gay_doggy_10eros", "lesbian_kiss_10eros"} {
		t.Run(sceneName, func(t *testing.T) {
			values := url.Values{
				"source_path": {"https://input.example/source.jpg"},
				"scene_name":  {sceneName},
			}
			request := httptest.NewRequest(
				http.MethodPost,
				publicTenErosImageToVideoPath,
				strings.NewReader(values.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Apikey", "api-key-1")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "target_path is required") {
				t.Fatalf("status/body = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestTenErosPublicRouteAcceptsAllFourScenes(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	tests := []struct {
		scene          string
		targetPath     string
		watermarkValue string
	}{
		{scene: "gay_doggy_10eros", targetPath: "https://input.example/doggy-target.jpg"},
		{scene: "gay_cumshot_10eros", watermarkValue: "false"},
		{scene: "gay_anal_creampie_10eros"},
		{scene: "lesbian_kiss_10eros", targetPath: "https://input.example/lesbian-target.jpg"},
	}

	for index, test := range tests {
		t.Run(test.scene, func(t *testing.T) {
			values := url.Values{
				"source_path": {"https://input.example/source.jpg"},
				"scene_name":  {test.scene},
				"task_id":     {fmt.Sprintf("accept-10eros-%d", index)},
			}
			if test.targetPath != "" {
				values.Set("target_path", test.targetPath)
			}
			if test.watermarkValue != "" {
				values.Set("is_watermark", test.watermarkValue)
			}
			request := httptest.NewRequest(http.MethodPost, publicTenErosImageToVideoPath, strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Apikey", "api-key-1")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
			}
			var public PublicTaskResponse
			if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if public.TaskType != WorkflowTenErosImageVideo || public.SceneName != test.scene {
				t.Fatalf("task type/scene = %q/%q", public.TaskType, public.SceneName)
			}
			if test.watermarkValue == "false" && (public.IsWatermark == nil || *public.IsWatermark) {
				t.Fatalf("is_watermark = %#v, want explicit false", public.IsWatermark)
			}
		})
	}
}

func TestTenErosPublicRouteRejectsMissingAPIKey(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	values := url.Values{
		"source_path": {"https://input.example/source.jpg"},
		"scene_name":  {"gay_cumshot_10eros"},
	}
	request := httptest.NewRequest(http.MethodPost, publicTenErosImageToVideoPath, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Apikey is required") {
		t.Fatalf("status/body = %d %q", response.Code, response.Body.String())
	}
}

func TestTenErosPublicRouteRejectsInvalidVideoFormat(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	values := url.Values{
		"source_path":  {"https://input.example/source.jpg"},
		"scene_name":   {"gay_cumshot_10eros"},
		"video_format": {"image/webp"},
	}
	request := httptest.NewRequest(http.MethodPost, publicTenErosImageToVideoPath, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Apikey", "api-key-1")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "video_format must be") {
		t.Fatalf("status/body = %d %q", response.Code, response.Body.String())
	}
}

func TestLegacyPublicRouteStillAcceptsLegacyScene(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	values := url.Values{
		"source_path":      {"https://input.example/source.jpg"},
		"scene_name":       {"goal_kick_portugal"},
		"video_scene_name": {"disney_real_anime_greet"},
		"task_id":          {"accept-legacy"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/public/generate/undress/anime/video", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Apikey", "api-key-1")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var public PublicTaskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if public.TaskType != WorkflowAnimeUndressVideo {
		t.Fatalf("task_type = %q, want %q", public.TaskType, WorkflowAnimeUndressVideo)
	}
}

func TestTenErosAndLegacyPublicRoutesAreSeparated(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	tests := []struct {
		name string
		path string
		form url.Values
		want string
	}{
		{
			name: "10eros scene is rejected by legacy route",
			path: "/api/public/generate/undress/anime/video",
			form: url.Values{
				"source_path": {"https://input.example/source.jpg"},
				"target_path": {"https://input.example/target.jpg"},
				"scene_name":  {"gay_doggy_10eros"},
			},
			want: publicTenErosImageToVideoPath,
		},
		{
			name: "10eros scene is rejected by legacy workflow alias",
			path: "/api/public/workflow/undress/anime/video",
			form: url.Values{
				"source_path": {"https://input.example/source.jpg"},
				"target_path": {"https://input.example/target.jpg"},
				"scene_name":  {"lesbian_kiss_10eros"},
			},
			want: publicTenErosImageToVideoPath,
		},
		{
			name: "legacy scene is rejected by 10eros route",
			path: publicTenErosImageToVideoPath,
			form: url.Values{
				"source_path": {"https://input.example/source.jpg"},
				"scene_name":  {"goal_kick_portugal"},
			},
			want: "not supported by the 10eros",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("status/body = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestTenErosPublishedContractMatchesCode(t *testing.T) {
	raw, err := os.ReadFile("docs/flowbridge-10eros-openapi.json")
	if err != nil {
		t.Fatalf("read OpenAPI document: %v", err)
	}
	var document struct {
		Paths map[string]struct {
			Post struct {
				Parameters []struct {
					Name string   `json:"name"`
					Enum []string `json:"enum"`
				} `json:"parameters"`
			} `json:"post"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode OpenAPI document: %v", err)
	}
	operation, ok := document.Paths[publicTenErosImageToVideoPath]
	if !ok {
		t.Fatalf("OpenAPI document is missing %s", publicTenErosImageToVideoPath)
	}
	var documentedScenes []string
	for _, parameter := range operation.Post.Parameters {
		if parameter.Name == "scene_name" {
			documentedScenes = append(documentedScenes, parameter.Enum...)
			break
		}
	}
	expectedScenes := make([]string, 0, len(tenErosBackendWorkflowSpecs))
	for sceneName := range tenErosBackendWorkflowSpecs {
		expectedScenes = append(expectedScenes, sceneName)
	}
	sort.Strings(documentedScenes)
	sort.Strings(expectedScenes)
	if !reflect.DeepEqual(documentedScenes, expectedScenes) {
		t.Fatalf("OpenAPI scenes = %#v, want %#v", documentedScenes, expectedScenes)
	}
	if _, ok := document.Paths[publicTaskDetailsPath]; !ok {
		t.Fatalf("OpenAPI document is missing %s", publicTaskDetailsPath)
	}

	for _, path := range []string{"README.md", "docs/10eros-image-to-video-api.md", "scripts/run_all_scenes.sh"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), publicTenErosImageToVideoPath) {
			t.Errorf("%s does not reference %s", path, publicTenErosImageToVideoPath)
		}
		for _, sceneName := range expectedScenes {
			if !strings.Contains(string(content), sceneName) {
				t.Errorf("%s does not reference scene %s", path, sceneName)
			}
		}
	}
	for _, path := range []string{"README.md", "docs/10eros-image-to-video-api.md"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), publicTaskDetailsPath) {
			t.Errorf("%s does not reference %s", path, publicTaskDetailsPath)
		}
	}
}

func assertValuesEqual(t *testing.T, actual url.Values, expected url.Values) {
	t.Helper()
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("form mismatch\nactual:   %#v\nexpected: %#v", actual, expected)
	}
}
