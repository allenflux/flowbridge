package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var tenErosBatch2Scenes = []struct {
	name               string
	requiresTargetPath bool
}{
	{name: "gay_oral_cumshot_10eros"},
	{name: "lesbian_cunnilingus_10eros"},
	{name: "gay_bondage_10eros"},
	{name: "gay_crossdressing_10eros"},
	{name: "gay_butt_slap_10eros"},
	{name: "gay_kneeling_doggy_10eros"},
	{name: "lesbian_strap_on_10eros", requiresTargetPath: true},
	{name: "lesbian_doggy_10eros", requiresTargetPath: true},
	{name: "lesbian_cowgirl_10eros", requiresTargetPath: true},
	{name: tenErosBatch2GayBarDoggyScene, requiresTargetPath: true},
}

func TestTenErosBatch2SpecsAreExactAndIsolated(t *testing.T) {
	if len(tenErosBatch2BackendWorkflowSpecs) != len(tenErosBatch2Scenes) {
		t.Fatalf("batch-2 scene count = %d, want %d", len(tenErosBatch2BackendWorkflowSpecs), len(tenErosBatch2Scenes))
	}
	for _, scene := range tenErosBatch2Scenes {
		spec, ok := tenErosBatch2BackendWorkflowSpecs[scene.name]
		if !ok {
			t.Errorf("batch-2 registry is missing %q", scene.name)
			continue
		}
		if spec.ImagePath != backendQwenTwoImagePath || spec.VideoPath != backendLTX8sVideoPath {
			t.Errorf("%s routes = %q -> %q", scene.name, spec.ImagePath, spec.VideoPath)
		}
		if spec.RequiresTargetPath != scene.requiresTargetPath {
			t.Errorf("%s RequiresTargetPath = %t, want %t", scene.name, spec.RequiresTargetPath, scene.requiresTargetPath)
		}
		if _, exists := tenErosBackendWorkflowSpecs[scene.name]; exists {
			t.Errorf("scene %q overlaps the first 10Eros registry", scene.name)
		}
		if _, exists := minimaxH3BackendWorkflowSpecs[scene.name]; exists {
			t.Errorf("scene %q overlaps the Minimax H3 registry", scene.name)
		}
	}
	for sceneName := range tenErosBackendWorkflowSpecs {
		if _, exists := minimaxH3BackendWorkflowSpecs[sceneName]; exists {
			t.Errorf("scene %q overlaps the first 10Eros and Minimax H3 registries", sceneName)
		}
	}
}

func TestTenErosBatch2WorkflowForwardsSingleAndTwoImageScenes(t *testing.T) {
	tests := []struct {
		name              string
		scene             string
		targetPath        string
		wantScene         string
		wantTargetForward bool
	}{
		{name: "two image", scene: "lesbian_strap_on_10eros", targetPath: "https://input.example/person-2.jpg", wantTargetForward: true},
		{name: "single image", scene: "gay_oral_cumshot_10eros", targetPath: "https://input.example/ignored-for-single-image.jpg"},
		{
			name:              "stored legacy gay bar spelling",
			scene:             legacyGayBarDoggyScene,
			targetPath:        "https://input.example/person-2.jpg",
			wantScene:         tenErosBatch2GayBarDoggyScene,
			wantTargetForward: true,
		},
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

			audioOff := false
			watermarkOff := false
			req := AnimeVideoRequest{
				SourcePath:         "https://input.example/person-1.jpg",
				TargetPath:         test.targetPath,
				SceneName:          test.scene,
				VideoSceneName:     test.scene,
				QwenIncomingPrompt: "image prompt",
				WanIncomingPrompt:  "video prompt",
				VideoFormat:        "video/h265-mp4",
				AudioEnabled:       &audioOff,
				IsWatermark:        &watermarkOff,
				IsEncrypt:          true,
				BID:                "batch-2-bid",
				AppID:              "batch-2-app",
				Fee:                "12",
				Title:              "batch-2-title",
				HashKey:            "batch-2-hash",
				APIKey:             "batch-2-api-key",
				NotifyURL:          "https://callback.example/batch-2",
				TaskID:             "batch-2-" + strings.ReplaceAll(test.name, " ", "-"),
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			task, err := store.CreateAnimeVideoTaskForWorkflow(context.Background(), req.TaskID, WorkflowTenErosBatch2ImageVideo, req, raw)
			if err != nil {
				t.Fatalf("CreateAnimeVideoTaskForWorkflow: %v", err)
			}
			if task.WorkflowType != WorkflowTenErosBatch2ImageVideo {
				t.Fatalf("workflow type = %q", task.WorkflowType)
			}

			backend := NewBackendClient(cfg)
			backend.client.Transport = roundTripFunc(recorder.roundTrip)
			worker := NewWorker(store, backend, cfg)
			if err := worker.runTask(context.Background(), task.ID); err != nil {
				t.Fatalf("runTask: %v", err)
			}

			requests := recorder.snapshot()
			if len(requests) != 2 {
				t.Fatalf("backend POST count = %d, want 2", len(requests))
			}
			if requests[0].Path != backendQwenTwoImagePath || requests[1].Path != backendLTX8sVideoPath {
				t.Fatalf("backend routes = %q -> %q", requests[0].Path, requests[1].Path)
			}
			wantScene := defaultString(test.wantScene, req.SceneName)
			for index, request := range requests {
				if request.APIKey != req.APIKey {
					t.Errorf("request %d Apikey = %q", index, request.APIKey)
				}
				if request.Form.Get("scene_name") != wantScene {
					t.Errorf("request %d scene_name = %q", index, request.Form.Get("scene_name"))
				}
			}
			wantTargetPath := ""
			if test.wantTargetForward {
				wantTargetPath = req.TargetPath
			}
			if requests[0].Form.Get("target_path") != wantTargetPath {
				t.Errorf("image target_path = %q, want %q", requests[0].Form.Get("target_path"), wantTargetPath)
			}
			if requests[0].Form.Get("is_encrypt") != "false" || requests[0].Form.Get("is_watermark") != "false" {
				t.Errorf("intermediate flags = encrypt:%q watermark:%q", requests[0].Form.Get("is_encrypt"), requests[0].Form.Get("is_watermark"))
			}
			if _, present := requests[0].Form["video_format"]; present {
				t.Errorf("intermediate request unexpectedly received video_format=%q", requests[0].Form.Get("video_format"))
			}
			if requests[1].Form.Get("source_path") != "https://cdn.example/intermediate.jpg" {
				t.Errorf("video source_path = %q", requests[1].Form.Get("source_path"))
			}
			if requests[1].Form.Get("is_encrypt") != "true" || requests[1].Form.Get("is_watermark") != "false" {
				t.Errorf("final flags = encrypt:%q watermark:%q", requests[1].Form.Get("is_encrypt"), requests[1].Form.Get("is_watermark"))
			}
			if requests[1].Form.Get("audio_enabled") != "false" || requests[1].Form.Get("video_format") != req.VideoFormat {
				t.Errorf("final audio/video format = %q/%q", requests[1].Form.Get("audio_enabled"), requests[1].Form.Get("video_format"))
			}
		})
	}
}

func TestTenErosBatch2PublicRouteAcceptsAllScenes(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))

	for index, scene := range tenErosBatch2Scenes {
		t.Run(scene.name, func(t *testing.T) {
			values := url.Values{
				"source_path": {"https://input.example/person-1.jpg"},
				"scene_name":  {scene.name},
				"task_id":     {"accept-batch-2-" + string(rune('a'+index))},
			}
			if scene.requiresTargetPath {
				values.Set("target_path", "https://input.example/person-2.jpg")
			}
			request := httptest.NewRequest(http.MethodPost, publicTenErosBatch2ImageToVideoPath, strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Apikey", "api-key-batch-2")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var public PublicTaskResponse
			if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if public.TaskType != WorkflowTenErosBatch2ImageVideo || public.SceneName != scene.name {
				t.Fatalf("task type/scene = %q/%q", public.TaskType, public.SceneName)
			}
			detail, err := store.GetTaskDetail(context.Background(), public.TaskID)
			if err != nil {
				t.Fatalf("GetTaskDetail: %v", err)
			}
			stored := requestFromRaw(detail.RequestPayload)
			if stored.VideoSceneName != scene.name {
				t.Fatalf("stored video_scene_name = %q", stored.VideoSceneName)
			}
		})
	}
}

func TestTenErosBatch2ValidationAndRouteIsolation(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))

	for _, scene := range tenErosBatch2Scenes {
		if !scene.requiresTargetPath {
			continue
		}
		t.Run("missing target "+scene.name, func(t *testing.T) {
			values := url.Values{"source_path": {"https://input.example/person.jpg"}, "scene_name": {scene.name}}
			request := httptest.NewRequest(http.MethodPost, publicTenErosBatch2ImageToVideoPath, strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Apikey", "api-key-batch-2")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "target_path is required") {
				t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
			}
		})
	}

	tests := []struct {
		name       string
		path       string
		form       url.Values
		wantStatus int
		wantBody   string
	}{
		{
			name:       "legacy typo rejected by batch-2 route",
			path:       publicTenErosBatch2ImageToVideoPath,
			form:       url.Values{"source_path": {"https://input.example/person.jpg"}, "target_path": {"https://input.example/target.jpg"}, "scene_name": {legacyGayBarDoggyScene}},
			wantStatus: http.StatusBadRequest,
			wantBody:   "not supported by the 10eros batch-2",
		},
		{
			name:       "batch-2 scene rejected by legacy route",
			path:       "/api/public/generate/undress/anime/video",
			form:       url.Values{"source_path": {"https://input.example/person.jpg"}, "scene_name": {"gay_crossdressing_10eros"}},
			wantStatus: http.StatusBadRequest,
			wantBody:   publicTenErosBatch2ImageToVideoPath,
		},
		{
			name:       "batch-2 scene rejected by first-batch route",
			path:       publicTenErosImageToVideoPath,
			form:       url.Values{"source_path": {"https://input.example/person.jpg"}, "scene_name": {"gay_crossdressing_10eros"}},
			wantStatus: http.StatusBadRequest,
			wantBody:   publicTenErosBatch2ImageToVideoPath,
		},
		{
			name:       "first-batch scene rejected by batch-2 route",
			path:       publicTenErosBatch2ImageToVideoPath,
			form:       url.Values{"source_path": {"https://input.example/person.jpg"}, "target_path": {"https://input.example/target.jpg"}, "scene_name": {"gay_doggy_10eros"}},
			wantStatus: http.StatusBadRequest,
			wantBody:   publicTenErosImageToVideoPath,
		},
		{
			name:       "legacy scene rejected by batch-2 route",
			path:       publicTenErosBatch2ImageToVideoPath,
			form:       url.Values{"source_path": {"https://input.example/person.jpg"}, "scene_name": {"goal_kick_portugal"}},
			wantStatus: http.StatusBadRequest,
			wantBody:   "not supported by the 10eros batch-2",
		},
		{
			name: "mismatched video scene rejected",
			path: publicTenErosBatch2ImageToVideoPath,
			form: url.Values{
				"source_path":      {"https://input.example/person.jpg"},
				"scene_name":       {"gay_crossdressing_10eros"},
				"video_scene_name": {"gay_butt_slap_10eros"},
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   "must equal scene_name",
		},
		{
			name: "invalid video format rejected",
			path: publicTenErosBatch2ImageToVideoPath,
			form: url.Values{
				"source_path":  {"https://input.example/person.jpg"},
				"scene_name":   {"gay_crossdressing_10eros"},
				"video_format": {"image/webp"},
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   "video_format must be",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Apikey", "api-key-batch-2")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("status/body = %d/%s, want %d containing %q", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestStoredWorkflowTypePreventsRouteDriftAfterSceneRegistryExpansion(t *testing.T) {
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
		SourcePath:   "https://input.example/person.jpg",
		SceneName:    "gay_crossdressing_10eros",
		OutputFormat: "video",
		VideoFormat:  "video/h264-mp4",
		APIKey:       "api-key-route-drift",
		TaskID:       "legacy-before-batch-2",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	task, err := store.CreateAnimeVideoTaskForWorkflow(context.Background(), req.TaskID, WorkflowAnimeUndressVideo, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTaskForWorkflow: %v", err)
	}
	recorder := &backendRecorder{}
	backend := NewBackendClient(cfg)
	backend.client.Transport = roundTripFunc(recorder.roundTrip)
	worker := NewWorker(store, backend, cfg)
	if err := worker.runTask(context.Background(), task.ID); err != nil {
		t.Fatalf("runTask: %v", err)
	}
	requests := recorder.snapshot()
	if len(requests) != 2 || requests[0].Path != backendUndressAnimeImagePath || requests[1].Path != backendUndressAnimeVideoPath {
		t.Fatalf("legacy persisted task routes = %#v", requests)
	}
}

func TestTenErosBatch2PublishedContractMatchesCode(t *testing.T) {
	raw, err := os.ReadFile("docs/flowbridge-10eros-batch2-openapi.json")
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
	operation, ok := document.Paths[publicTenErosBatch2ImageToVideoPath]
	if !ok {
		t.Fatalf("OpenAPI document is missing %s", publicTenErosBatch2ImageToVideoPath)
	}
	var documentedScenes []string
	for _, parameter := range operation.Post.Parameters {
		if parameter.Name == "scene_name" {
			documentedScenes = append(documentedScenes, parameter.Enum...)
		}
	}
	expectedScenes := make([]string, 0, len(tenErosBatch2BackendWorkflowSpecs))
	for sceneName := range tenErosBatch2BackendWorkflowSpecs {
		expectedScenes = append(expectedScenes, sceneName)
	}
	sort.Strings(documentedScenes)
	sort.Strings(expectedScenes)
	if strings.Join(documentedScenes, "\n") != strings.Join(expectedScenes, "\n") {
		t.Fatalf("documented scenes = %#v, want %#v", documentedScenes, expectedScenes)
	}
	for _, path := range []string{"README.md", "docs/10eros-batch2-image-to-video-api.md", "scripts/run_batch2_scenes.sh"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if !strings.Contains(string(content), publicTenErosBatch2ImageToVideoPath) {
			t.Errorf("%s does not reference %s", path, publicTenErosBatch2ImageToVideoPath)
		}
	}
}
