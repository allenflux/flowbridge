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
	"time"
)

func TestParseAnimeVideoRequestCapturesOnlyActualHeaders(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/x-www-form-urlencoded"} {
		t.Run(contentType, func(t *testing.T) {
			body := `{"source_path":"https://input.example/source.jpg","apikey":"body-key","backend_headers":{"Authorization":["injected"],"X-Injected":["injected"]}}`
			if contentType != "application/json" {
				body = "source_path=https%3A%2F%2Finput.example%2Fsource.jpg&apikey=body-key&backend_headers=%7B%22X-Injected%22%3A%5B%22injected%22%5D%7D"
			}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Authorization", "Bearer actual-token")
			request.Header.Set("Apikey", "header-key")
			request.Header.Set("X-API-KEY", "alternate-key")
			request.Header.Set("X-SECRET-KEY", "preferred-secret")
			request.Header.Set("SECRETKEY", "legacy-secret")
			request.Header["X-Future-Auth"] = []string{"first", "second"}
			request.Header.Set("Connection", "X-Private-Hop")
			request.Header.Set("X-Private-Hop", "must-drop")
			before := request.Header.Clone()

			parsed, err := parseAnimeVideoRequest(request)
			if err != nil {
				t.Fatalf("parseAnimeVideoRequest: %v", err)
			}
			if parsed.APIKey != "body-key" {
				t.Errorf("APIKey = %q, want body-key", parsed.APIKey)
			}
			want := filterBackendHeaders(before)
			if !reflect.DeepEqual(parsed.BackendHeaders, want) {
				t.Fatalf("captured headers = %#v, want %#v", parsed.BackendHeaders, want)
			}
			if parsed.BackendHeaders.Get("X-Injected") != "" || parsed.BackendHeaders.Get("Authorization") != "Bearer actual-token" {
				t.Fatalf("body injected backend headers: %#v", parsed.BackendHeaders)
			}
			if !reflect.DeepEqual(request.Header, before) {
				t.Fatalf("parse modified incoming headers: %#v", request.Header)
			}
			request.Header["X-Future-Auth"][0] = "incoming-mutated"
			if got := parsed.BackendHeaders.Values("X-Future-Auth"); !reflect.DeepEqual(got, []string{"first", "second"}) {
				t.Fatalf("snapshot shares incoming header values: %#v", got)
			}
			parsed.BackendHeaders["X-Future-Auth"][1] = "snapshot-mutated"
			if request.Header["X-Future-Auth"][1] != "second" {
				t.Fatal("snapshot mutation changed incoming header values")
			}
		})
	}
}

func TestFilterBackendHeadersDropsTransportAndRewrittenBodyMetadata(t *testing.T) {
	blocked := []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"TE", "Trailer", "Transfer-Encoding", "Upgrade", "Host", "Content-Type", "Content-Length",
		"Content-Encoding", "Accept-Encoding", "Accept", "User-Agent", "Content-MD5", "Content-Range", "Digest", "Expect",
		"Notify_URL", "Notify-URL",
	}
	headers := http.Header{
		"Authorization": {"Bearer token"},
		"Cookie":        {"session=one", "other=two"},
		"X-Future-Auth": {"one", "two"},
	}
	headers.Set("X-SECRET-KEY", "secret")
	headers.Set("SECRETKEY", "")
	for _, name := range blocked {
		// Header maps may contain noncanonical names, including after JSON recovery.
		headers[strings.ToLower(name)] = []string{"must-drop"}
	}
	headers["cOnNeCtIoN"] = []string{" X-Private-Hop, x-second-hop ", "X-Third-Hop"}
	headers["X-Private-Hop"] = []string{"must-drop"}
	headers["X-SECOND-HOP"] = []string{"must-drop"}
	headers["x-third-hop"] = []string{"must-drop"}
	before := headers.Clone()
	filtered := filterBackendHeaders(headers)
	for _, name := range append(blocked, "X-Private-Hop", "X-Second-Hop", "X-Third-Hop") {
		for actual := range filtered {
			if strings.EqualFold(actual, name) {
				t.Errorf("forwarded excluded header %q", actual)
			}
		}
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Future-Auth", "X-SECRET-KEY", "SECRETKEY"} {
		if !reflect.DeepEqual(filtered.Values(name), headers.Values(name)) {
			t.Errorf("%s values = %#v, want %#v", name, filtered.Values(name), headers.Values(name))
		}
	}
	if !reflect.DeepEqual(headers, before) {
		t.Fatal("filter modified original headers")
	}
	filtered["X-Future-Auth"][0] = "modified"
	if headers.Get("X-Future-Auth") != "one" {
		t.Fatal("filter result shares original value slices")
	}
	if got := filterBackendHeaders(nil); len(got) != 0 {
		t.Fatalf("filterBackendHeaders(nil) = %#v", got)
	}
}

func TestBackendClientForwardsHeadersWithOwnedDefaultsAndAPIKeyPrecedence(t *testing.T) {
	for _, apiKey := range []string{" body-key ", ""} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			t.Run(method+"/"+apiKey, func(t *testing.T) {
				cfg := defaultConfig()
				cfg.BackendBaseURL = "http://backend.example"
				backend := NewBackendClient(cfg)
				headers := http.Header{}
				headers.Set("Authorization", "Bearer token")
				headers.Set("Apikey", "header-key")
				headers.Set("X-API-KEY", "alternate-key")
				headers.Set("X-SECRET-KEY", "secret")
				headers.Set("SECRETKEY", "legacy-secret")
				headers["X-Future-Auth"] = []string{"one", "two"}
				headers.Set("Accept", "text/html")
				headers.Set("Content-Type", "application/json")
				headers.Set("Content-Length", "999")
				headers.Set("Content-Encoding", "gzip")
				headers.Set("User-Agent", "incoming-agent")
				headers.Set("Connection", "X-Hop-Auth")
				headers.Set("X-Hop-Auth", "must-drop")
				before := headers.Clone()
				calls := 0
				backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "curl/8.7.1" {
						t.Errorf("backend defaults were overridden: %#v", request.Header)
					}
					for _, name := range []string{"Authorization", "X-SECRET-KEY", "SECRETKEY", "X-Future-Auth"} {
						if !reflect.DeepEqual(request.Header.Values(name), headers.Values(name)) {
							t.Errorf("%s = %#v, want %#v", name, request.Header.Values(name), headers.Values(name))
						}
					}
					wantKey, wantAlternate := "header-key", "alternate-key"
					if strings.TrimSpace(apiKey) != "" {
						wantKey, wantAlternate = "body-key", "body-key"
					}
					if request.Header.Get("Apikey") != wantKey || request.Header.Get("X-API-KEY") != wantAlternate {
						t.Errorf("API key aliases = %q, %q", request.Header.Get("Apikey"), request.Header.Get("X-API-KEY"))
					}
					for _, name := range []string{"Content-Length", "Content-Encoding", "Connection", "X-Hop-Auth"} {
						if request.Header.Get(name) != "" {
							t.Errorf("forwarded %s = %q", name, request.Header.Get(name))
						}
					}
					if method == http.MethodPost {
						if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
							t.Errorf("POST Content-Type = %q", request.Header.Get("Content-Type"))
						}
						if err := request.ParseForm(); err != nil || request.PostForm.Get("source_path") != "source.jpg" {
							t.Errorf("backend POST form = %#v, error = %v", request.PostForm, err)
						}
					} else if request.URL.Query().Get("task_id") != "backend-task" || request.URL.Query().Get("apikey") != strings.TrimSpace(apiKey) {
						t.Errorf("backend query = %s", request.URL.RawQuery)
					}
					return testJSONResponse(request, http.StatusOK, `{"task_id":"backend-task","status":2}`), nil
				})
				var err error
				if method == http.MethodPost {
					_, _, err = backend.PostForm(context.Background(), "/submit", map[string]string{"source_path": "source.jpg"}, apiKey, headers)
				} else {
					_, _, err = backend.GetTask(context.Background(), "backend-task", apiKey, headers)
				}
				if err != nil || calls != 1 {
					t.Fatalf("backend call error = %v, calls = %d", err, calls)
				}
				if !reflect.DeepEqual(headers, before) {
					t.Fatal("backend client modified saved headers")
				}
			})
		}
	}
}

func TestBackendClientDoesNotForwardCredentialsAcrossOrigins(t *testing.T) {
	for _, redirect := range []struct {
		name, location string
		blocked        bool
	}{
		{name: "same origin", location: "/redirected"},
		{name: "different host", location: "http://another.example/redirected", blocked: true},
		{name: "different scheme", location: "https://backend.example/redirected", blocked: true},
		{name: "different port", location: "http://backend.example:81/redirected", blocked: true},
	} {
		t.Run(redirect.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.BackendBaseURL = "http://backend.example"
			backend := NewBackendClient(cfg)
			calls := 0
			backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					response := testJSONResponse(request, http.StatusTemporaryRedirect, `{}`)
					response.Header.Set("Location", redirect.location)
					return response, nil
				}
				if request.URL.Host != "backend.example" {
					t.Errorf("credentials reached another origin: %s", request.URL)
				}
				if request.Header.Get("X-Future-Auth") != "secret" {
					t.Errorf("same-origin redirect lost auth header: %#v", request.Header)
				}
				return testJSONResponse(request, http.StatusOK, `{"task_id":"backend-task","status":2}`), nil
			})
			_, _, err := backend.GetTask(context.Background(), "backend-task", "api-key", http.Header{"X-Future-Auth": {"secret"}})
			if redirect.blocked {
				if calls != 1 || err == nil {
					t.Fatalf("cross-origin redirect calls = %d, error = %v", calls, err)
				}
			} else if calls != 2 || err != nil {
				t.Fatalf("same-origin redirect calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestWorkerRetryAndSubmissionVisibilityRetainHeaders(t *testing.T) {
	_, store := newBatchTaskTestServer(t)
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.MaxSubmitRetries = 1
	backend := NewBackendClient(cfg)
	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: "retry-headers", APIKey: "api-key",
		BackendHeaders: http.Header{"X-Secret-Key": {"secret"}, "Authorization": {"Bearer token"}, "X-Future-Auth": {"one", "two"}},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	var methods []string
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		methods = append(methods, request.Method)
		for name, values := range req.BackendHeaders {
			if !reflect.DeepEqual(request.Header.Values(name), values) {
				t.Errorf("request %d lost %s: %#v", len(methods), name, request.Header.Values(name))
			}
		}
		if request.Header.Get("Apikey") != req.APIKey || request.Header.Get("X-API-KEY") != req.APIKey {
			t.Errorf("request %d lost API key aliases: %#v", len(methods), request.Header)
		}
		switch len(methods) {
		case 1:
			return testJSONResponse(request, http.StatusServiceUnavailable, `{"error":"unavailable"}`), nil
		case 2, 3:
			if request.URL.Query().Get("task_id") != "retry-headers_image" {
				t.Errorf("visibility query task_id = %q", request.URL.Query().Get("task_id"))
			}
			return testJSONResponse(request, http.StatusNotFound, `{"error":"not found"}`), nil
		default:
			return testJSONResponse(request, http.StatusOK, `{"task_id":"backend-task"}`), nil
		}
	})
	worker := NewWorker(store, backend, cfg)
	worker.submissionRecoveryWindow = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, result, err := worker.postBackendForm(ctx, task.ID, 1, "/submit", map[string]string{"task_id": "retry-headers_image"}, req.APIKey, req.BackendHeaders)
	if err != nil || backendTaskID(result) != "backend-task" {
		t.Fatalf("postBackendForm result = %#v, error = %v", result, err)
	}
	if want := []string{http.MethodPost, http.MethodGet, http.MethodGet, http.MethodPost}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("backend methods = %#v, want %#v", methods, want)
	}
}

func TestWorkerUsesPersistedHeadersForSubmissionsAndRecoveredPolling(t *testing.T) {
	for _, recovery := range []string{"fresh", "running-without-backend-id", "running-with-backend-id"} {
		t.Run(recovery, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.BackendBaseURL = "http://backend.example"
			cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
			cfg.PollInterval = time.Millisecond
			cfg.TaskTimeout = 2 * time.Second
			cfg.HTTPTimeout = time.Second
			store, err := OpenStore(cfg.DBPath)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			req := AnimeVideoRequest{
				SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal",
				VideoSceneName: "disney_real_anime_greet", APIKey: "api-key", TaskID: "forwarded-headers",
				BackendHeaders: http.Header{"X-Secret-Key": {"secret"}, "Authorization": {"Bearer token"}, "X-Future-Auth": {"one", "two"}},
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
			if err != nil {
				t.Fatalf("CreateAnimeVideoTask: %v", err)
			}
			if recovery != "fresh" {
				if err := store.UpdateStepStart(context.Background(), task.ID, 1, json.RawMessage(`{}`)); err != nil {
					t.Fatalf("UpdateStepStart: %v", err)
				}
				if recovery == "running-with-backend-id" {
					if err := store.UpdateStepAccepted(context.Background(), task.ID, 1, "image-task", json.RawMessage(`{"task_id":"image-task"}`)); err != nil {
						t.Fatalf("UpdateStepAccepted: %v", err)
					}
				}
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close original store: %v", err)
			}
			store, err = OpenStore(cfg.DBPath)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			backend := NewBackendClient(cfg)
			posts, polls := 0, 0
			backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				for name, values := range req.BackendHeaders {
					if !reflect.DeepEqual(request.Header.Values(name), values) {
						t.Errorf("%s %s lost %s: %#v", request.Method, request.URL, name, request.Header.Values(name))
					}
				}
				if request.Header.Get("Apikey") != req.APIKey || request.Header.Get("X-API-KEY") != req.APIKey {
					t.Errorf("missing API key on %s %s", request.Method, request.URL)
				}
				if request.Method == http.MethodPost {
					posts++
					backendID := "video-task"
					if request.URL.Path == backendUndressAnimeImagePath {
						backendID = "image-task"
					}
					return testJSONResponse(request, http.StatusOK, fmt.Sprintf(`{"task_id":%q}`, backendID)), nil
				}
				polls++
				backendID := request.URL.Query().Get("task_id")
				url := "https://cdn.example/final.mp4"
				if backendID == "image-task" || strings.HasSuffix(backendID, "_image") {
					url = "https://cdn.example/intermediate.jpg"
				}
				return testJSONResponse(request, http.StatusOK, fmt.Sprintf(`{"task_id":%q,"status":2,"out_data":[%q]}`, backendID, url)), nil
			})
			worker := NewWorker(store, backend, cfg)
			if err := worker.runTask(context.Background(), task.ID); err != nil {
				t.Fatalf("runTask: %v", err)
			}
			wantPosts, wantPolls := 2, 2
			if recovery != "fresh" {
				wantPosts = 1
			}
			if recovery == "running-without-backend-id" {
				wantPolls = 3
			}
			if posts != wantPosts || polls != wantPolls {
				t.Fatalf("POSTs = %d, polls = %d; want %d, %d", posts, polls, wantPosts, wantPolls)
			}
		})
	}
}

func TestWorkerDeadlineFinalCheckRetainsHeaders(t *testing.T) {
	cfg := defaultConfig()
	cfg.BackendBaseURL = "http://backend.example"
	cfg.HTTPTimeout = time.Second
	cfg.PollInterval = time.Second
	backend := NewBackendClient(cfg)
	requests := 0
	backend.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("X-Secret-Key") != "secret" || request.Header.Get("X-Future-Auth") != "future" {
			t.Errorf("deadline check lost credentials: %#v", request.Header)
		}
		if request.Context().Err() != nil {
			t.Errorf("backend request used expired context: %v", request.Context().Err())
		}
		if requests == 1 {
			return testJSONResponse(request, http.StatusOK, `{"task_id":"backend-task","status":1}`), nil
		}
		return testJSONResponse(request, http.StatusOK, `{"task_id":"backend-task","status":2}`), nil
	})
	worker := NewWorker(nil, backend, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, _, err := worker.waitBackendTask(ctx, 0, 1, "backend-task", "api-key", http.Header{"X-Secret-Key": {"secret"}, "X-Future-Auth": {"future"}})
	if err != nil || requests != 2 {
		t.Fatalf("waitBackendTask final check error = %v, requests = %d", err, requests)
	}
}

func TestPublicTaskResponsesDoNotExposeStoredAuthenticationHeaders(t *testing.T) {
	server, store := newBatchTaskTestServer(t)
	create := httptest.NewRequest(http.MethodPost, "/api/public/generate/undress/anime/video", strings.NewReader(`{"source_path":"https://input.example/source.jpg","scene_name":"goal_kick_portugal","task_id":"private-headers"}`))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Apikey", "private-api-key")
	create.Header.Set("X-Secret-Key", "private-secret")
	create.Header.Set("Authorization", "Bearer private-token")
	create.Header.Set("Cookie", "session=private-cookie")
	created := httptest.NewRecorder()
	server.ServeHTTP(created, create)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	detail, err := store.GetTaskDetail(context.Background(), "private-headers")
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	var stored AnimeVideoRequest
	if err := json.Unmarshal(detail.RequestPayload, &stored); err != nil || stored.BackendHeaders.Get("X-Secret-Key") != "private-secret" {
		t.Fatalf("headers were not persisted: %s, error = %v", detail.RequestPayload, err)
	}
	responses := []string{created.Body.String()}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/public/task?task_id=private-headers", nil),
		httptest.NewRequest(http.MethodPost, publicTaskDetailsPath, strings.NewReader(`["private-headers"]`)),
	} {
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("query status = %d: %s", response.Code, response.Body.String())
		}
		responses = append(responses, response.Body.String())
	}
	for _, response := range responses {
		for _, secret := range []string{"backend_headers", "private-api-key", "private-secret", "private-token", "private-cookie"} {
			if strings.Contains(response, secret) {
				t.Errorf("public response leaked %q: %s", secret, response)
			}
		}
	}
	admin := httptest.NewRecorder()
	server.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/admin/workflows/private-headers", nil))
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", admin.Code, admin.Body.String())
	}
	for _, secret := range []string{"private-api-key", "private-secret", "private-token", "private-cookie"} {
		if strings.Contains(admin.Body.String(), secret) {
			t.Errorf("admin response leaked %q", secret)
		}
	}
	after, err := store.GetTaskDetail(context.Background(), "private-headers")
	if err != nil {
		t.Fatalf("reload task after admin render: %v", err)
	}
	if !reflect.DeepEqual(after.RequestPayload, detail.RequestPayload) {
		t.Fatalf("admin redaction modified stored credentials: %s", after.RequestPayload)
	}
}
