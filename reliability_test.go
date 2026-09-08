package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type reliabilityRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn reliabilityRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNormalizeConfigEnforcesFiniteTimeouts(t *testing.T) {
	cfg := defaultConfig()
	cfg.RequestTimeout = 0
	cfg.TaskTimeout = 0
	cfg.MaxRunnableTasks = 0
	normalizeConfig(&cfg)
	if cfg.RequestTimeout != defaultRequestTimeout {
		t.Fatalf("RequestTimeout = %s, want %s", cfg.RequestTimeout, defaultRequestTimeout)
	}
	if cfg.TaskTimeout != 72*time.Hour {
		t.Fatalf("TaskTimeout = %s, want 72h", cfg.TaskTimeout)
	}
	if cfg.MaxRunnableTasks != cfg.WorkerQueueSize {
		t.Fatalf("MaxRunnableTasks = %d, want queue size %d", cfg.MaxRunnableTasks, cfg.WorkerQueueSize)
	}
}

func TestInMemorySQLiteDSNsKeepASingleConnection(t *testing.T) {
	for _, path := range []string{
		":memory:",
		":memory:?cache=shared",
		"file::memory:",
		"file::memory:?cache=shared",
		"file:flowbridge-tests?mode=memory&cache=shared",
	} {
		t.Run(path, func(t *testing.T) {
			if !isInMemorySQLite(path) {
				t.Fatalf("isInMemorySQLite(%q) = false", path)
			}
		})
	}
	for _, path := range []string{"flowbridge.db", "/tmp/mode=memory.db", "file:flowbridge.db?cache=shared"} {
		t.Run(path, func(t *testing.T) {
			if isInMemorySQLite(path) {
				t.Fatalf("isInMemorySQLite(%q) = true", path)
			}
		})
	}

	store, err := OpenStore("file::memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if maximum := store.db.Stats().MaxOpenConnections; maximum != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", maximum)
	}
}

func TestExpiredWorkflowDeadlineIsNotResetOnRecovery(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.TaskTimeout = 20 * time.Millisecond
	cfg.HTTPTimeout = 100 * time.Millisecond

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg",
		SceneName:  "goal_kick_portugal",
		TaskID:     "expired-recovery-task",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	if err := store.MarkTaskRunning(context.Background(), task.ID, StepAnimeImage); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	if err := store.UpdateStepStart(context.Background(), task.ID, 1, json.RawMessage(`{"source_path":"test"}`)); err != nil {
		t.Fatalf("UpdateStepStart: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE workflow_tasks SET created_at = ?, deadline_anchor = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second), time.Now().UTC().Add(-time.Second), task.ID); err != nil {
		t.Fatalf("age workflow task: %v", err)
	}

	backendCalls := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = reliabilityRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		backendCalls++
		return nil, errors.New("backend must not be called after the workflow deadline")
	})
	worker := NewWorker(store, backend, cfg)

	err = worker.runTask(context.Background(), task.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runTask error = %v, want context deadline exceeded", err)
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls = %d, want 0", backendCalls)
	}
	stored, err := store.GetPublicTask(context.Background(), req.TaskID)
	if err != nil {
		t.Fatalf("GetPublicTask: %v", err)
	}
	if stored.Status != StatusFailed || !strings.Contains(stored.ErrorMessage, "deadline") {
		t.Fatalf("stored status/error = %d/%q", stored.Status, stored.ErrorMessage)
	}
	step, err := store.GetStep(context.Background(), task.ID, 1)
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if step.Status != StatusFailed || step.FinishedAt == nil || !strings.Contains(step.ErrorMessage, "deadline") {
		t.Fatalf("step status/finished/error = %d/%v/%q", step.Status, step.FinishedAt, step.ErrorMessage)
	}
}

func TestMigrationGrandfathersExistingRunnableTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE workflow_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL UNIQUE,
		workflow_type TEXT NOT NULL,
		status INTEGER NOT NULL,
		current_step TEXT NOT NULL DEFAULT '',
		request_payload TEXT NOT NULL DEFAULT '{}',
		final_result TEXT,
		error_message TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		finished_at DATETIME
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	old := time.Now().UTC().Add(-24 * time.Hour)
	result, err := db.Exec(`INSERT INTO workflow_tasks
		(task_id, workflow_type, status, current_step, request_payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, '{}', ?, ?)`,
		"legacy-running", WorkflowAnimeUndressVideo, StatusRunning, StepAnimeImage, old, old)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy task: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("legacy task id: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	migrationStarted := time.Now().UTC().Add(-time.Second)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore legacy database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task, err := store.GetTaskByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTaskByID: %v", err)
	}
	if task.DeadlineAnchor.Before(migrationStarted) {
		t.Fatalf("deadline anchor = %s, want a fresh migration-time anchor", task.DeadlineAnchor)
	}
}

func TestRecoverableStepPersistenceErrorsKeepTaskRunnable(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		operation func(*Worker, int64) error
	}{
		{
			name: "accepted backend task",
			operation: func(worker *Worker, taskID int64) error {
				return worker.updateStepAccepted(taskID, 1, "backend-id", json.RawMessage(`{"status":0}`))
			},
		},
		{
			name: "successful backend task",
			operation: func(worker *Worker, taskID int64) error {
				return worker.markStepSuccess(taskID, 1, json.RawMessage(`{"status":2}`))
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
			cfg.HTTPTimeout = 20 * time.Millisecond
			store, err := OpenStore(cfg.DBPath)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			req := AnimeVideoRequest{SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: "persistence-" + strings.ReplaceAll(testCase.name, " ", "-")}
			raw, _ := json.Marshal(req)
			task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
			if err != nil {
				t.Fatalf("CreateAnimeVideoTask: %v", err)
			}
			if err := store.MarkTaskRunning(context.Background(), task.ID, StepAnimeImage); err != nil {
				t.Fatalf("MarkTaskRunning: %v", err)
			}
			if err := store.UpdateStepStart(context.Background(), task.ID, 1, json.RawMessage(`{}`)); err != nil {
				t.Fatalf("UpdateStepStart: %v", err)
			}

			connections := make([]*sql.Conn, 0, storeMaxOpenConnections)
			for range storeMaxOpenConnections {
				connection, err := store.db.Conn(context.Background())
				if err != nil {
					t.Fatalf("reserve database connection: %v", err)
				}
				connections = append(connections, connection)
			}
			worker := NewWorker(store, NewBackendClient(cfg), cfg)
			persistenceErr := testCase.operation(worker, task.ID)
			for _, connection := range connections {
				_ = connection.Close()
			}
			var recoverable *recoverablePersistenceError
			if !errors.As(persistenceErr, &recoverable) {
				t.Fatalf("operation error = %v, want recoverable persistence error", persistenceErr)
			}
			worker.handleTaskError(context.Background(), context.Background(), task.ID, persistenceErr)
			stored, err := store.GetPublicTask(context.Background(), req.TaskID)
			if err != nil {
				t.Fatalf("GetPublicTask: %v", err)
			}
			if stored.Status != StatusRunning {
				t.Fatalf("task status = %d, want runnable status %d", stored.Status, StatusRunning)
			}
		})
	}
}

func TestTerminalStepFailureClosesTaskAndStepTogether(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	req := AnimeVideoRequest{SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: "atomic-terminal-state"}
	raw, _ := json.Marshal(req)
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	if err := store.MarkTaskRunning(context.Background(), task.ID, StepAnimeImage); err != nil {
		t.Fatalf("MarkTaskRunning: %v", err)
	}
	if err := store.UpdateStepStart(context.Background(), task.ID, 1, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("UpdateStepStart: %v", err)
	}

	worker := NewWorker(store, NewBackendClient(cfg), cfg)
	backendResult := json.RawMessage(`{"status":-1,"message":"backend rejected task"}`)
	worker.handleTaskError(context.Background(), context.Background(), task.ID,
		worker.failStep(1, errors.New("backend task failed"), backendResult))

	stored, err := store.GetPublicTask(context.Background(), req.TaskID)
	if err != nil {
		t.Fatalf("GetPublicTask: %v", err)
	}
	step, err := store.GetStep(context.Background(), task.ID, 1)
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if stored.Status != StatusFailed || stored.FinishedAt == nil {
		t.Fatalf("task status/finished = %d/%v", stored.Status, stored.FinishedAt)
	}
	if step.Status != StatusFailed || step.FinishedAt == nil || string(step.ResultPayload) != string(backendResult) {
		t.Fatalf("step status/finished/result = %d/%v/%s", step.Status, step.FinishedAt, step.ResultPayload)
	}
}

func TestUnknownBackendStatusFailsAfterConfiguredLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.MaxPollErrors = 3
	cfg.PollInterval = time.Millisecond
	cfg.HTTPTimeout = time.Second

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	queries := 0
	backend := NewBackendClient(cfg)
	backend.client.Transport = reliabilityRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		queries++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"task_id":"backend-task","status":99}`)),
			Request:    request,
		}, nil
	})
	worker := NewWorker(store, backend, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = worker.waitBackendTask(ctx, 0, 1, "backend-task", "api-key")
	if err == nil || !strings.Contains(err.Error(), "missing or unknown status") {
		t.Fatalf("waitBackendTask error = %v", err)
	}
	if queries != cfg.MaxPollErrors {
		t.Fatalf("backend queries = %d, want %d", queries, cfg.MaxPollErrors)
	}
}

func TestRequestTimeoutBoundsSQLiteWait(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.RequestTimeout = 20 * time.Millisecond

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for range storeMaxOpenConnections {
		connection, err := store.db.Conn(context.Background())
		if err != nil {
			t.Fatalf("reserve database connection: %v", err)
		}
		defer connection.Close()
	}

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	request := httptest.NewRequest(http.MethodGet, "/api/public/task?task_id=blocked", nil)
	response := httptest.NewRecorder()
	started := time.Now()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request took %s, want a bounded wait", elapsed)
	}
}

func TestWorkflowAdmissionRejectsAnOverfullBacklog(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.MaxRunnableTasks = 1
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	existing := AnimeVideoRequest{
		SourcePath: "https://input.example/existing.jpg",
		SceneName:  "goal_kick_portugal",
		TaskID:     "existing-runnable",
	}
	raw, _ := json.Marshal(existing)
	if _, err := store.CreateAnimeVideoTask(context.Background(), existing.TaskID, existing, raw); err != nil {
		t.Fatalf("CreateAnimeVideoTask: %v", err)
	}

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	request := httptest.NewRequest(http.MethodPost, "/api/public/generate/undress/anime/video",
		strings.NewReader(`{"source_path":"https://input.example/new.jpg","scene_name":"goal_kick_portugal","task_id":"must-be-rejected"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q, want 5", response.Header().Get("Retry-After"))
	}
	if !strings.Contains(response.Body.String(), "backlog is full") {
		t.Fatalf("response = %s", response.Body.String())
	}
	count, err := store.CountTasks(context.Background())
	if err != nil {
		t.Fatalf("CountTasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("task count = %d, want 1", count)
	}
}

func TestWorkerWaitsForShutdown(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	cfg.WorkerConcurrency = 2

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	worker := NewWorker(store, NewBackendClient(cfg), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := worker.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestStoreSupportsConcurrentReadersAndWriters(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const goroutines = 8
	const tasksPerGoroutine = 20
	errorsCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for workerIndex := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for taskIndex := range tasksPerGoroutine {
				taskID := "concurrent-" + string(rune('a'+workerIndex)) + "-" + time.Unix(int64(taskIndex), 0).Format("150405")
				req := AnimeVideoRequest{SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: taskID}
				raw, err := json.Marshal(req)
				if err != nil {
					errorsCh <- err
					return
				}
				task, err := store.CreateAnimeVideoTask(context.Background(), taskID, req, raw)
				if err != nil {
					errorsCh <- err
					return
				}
				if err := store.MarkTaskRunning(context.Background(), task.ID, StepAnimeImage); err != nil {
					errorsCh <- err
					return
				}
				if _, err := store.GetPublicTask(context.Background(), taskID); err != nil {
					errorsCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent store operation: %v", err)
	}
}

func TestRunnableTaskScanUsesPartialIndex(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.db.QueryContext(context.Background(), `EXPLAIN QUERY PLAN
		SELECT id FROM workflow_tasks INDEXED BY idx_workflow_tasks_runnable_id
		WHERE status IN (0, 1) ORDER BY id ASC LIMIT 32`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if value := plan.String(); !strings.Contains(value, "idx_workflow_tasks_runnable_id") || strings.Contains(value, "TEMP B-TREE") {
		t.Fatalf("unexpected runnable task query plan:\n%s", value)
	}
}

func TestBatchResultPayloadLimitPreventsMemoryAmplification(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskIDs []string
	for _, taskID := range []string{"large-result-a", "large-result-b"} {
		req := AnimeVideoRequest{SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: taskID}
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		task, err := store.CreateAnimeVideoTask(context.Background(), taskID, req, raw)
		if err != nil {
			t.Fatalf("CreateAnimeVideoTask: %v", err)
		}
		result := json.RawMessage(`{"status":2,"out_data":"` + strings.Repeat("x", 96) + `"}`)
		if err := store.MarkTaskSuccess(context.Background(), task.ID, result); err != nil {
			t.Fatalf("MarkTaskSuccess: %v", err)
		}
		taskIDs = append(taskIDs, taskID)
	}

	if _, err := store.getTasksByTaskIDs(context.Background(), taskIDs, 128); !errors.Is(err, ErrBatchResultTooLarge) {
		t.Fatalf("getTasksByTaskIDs error = %v, want ErrBatchResultTooLarge", err)
	}
}

func TestBootstrapFaviconIsEmbeddedAndServedLocally(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(cfg, store, NewWorker(store, NewBackendClient(cfg), cfg))
	request := httptest.NewRequest(http.MethodGet, "/favicon.svg?v=1.13.1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/svg+xml" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if !strings.Contains(response.Body.String(), "bi-arrow-left-right") {
		t.Fatalf("favicon is not the expected Bootstrap icon: %s", response.Body.String())
	}
}

func BenchmarkTaskStatusLookup(b *testing.B) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(b.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		b.Fatalf("OpenStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	req := AnimeVideoRequest{
		SourcePath: "https://input.example/source.jpg",
		SceneName:  "goal_kick_portugal",
		TaskID:     "benchmark-task",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		b.Fatalf("marshal request: %v", err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw)
	if err != nil {
		b.Fatalf("CreateAnimeVideoTask: %v", err)
	}
	largePayload := json.RawMessage(`{"blob":"` + strings.Repeat("x", 1<<20) + `"}`)
	if err := store.MarkStepSuccess(context.Background(), task.ID, 1, largePayload); err != nil {
		b.Fatalf("MarkStepSuccess: %v", err)
	}

	b.Run("public_status", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := store.GetPublicTask(context.Background(), req.TaskID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full_admin_detail", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := store.GetTaskDetail(context.Background(), req.TaskID); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkConcurrentPublicTaskLookup(b *testing.B) {
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(b.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		b.Fatalf("OpenStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	req := AnimeVideoRequest{SourcePath: "https://input.example/source.jpg", SceneName: "goal_kick_portugal", TaskID: "concurrent-benchmark-task"}
	raw, err := json.Marshal(req)
	if err != nil {
		b.Fatalf("marshal request: %v", err)
	}
	if _, err := store.CreateAnimeVideoTask(context.Background(), req.TaskID, req, raw); err != nil {
		b.Fatalf("CreateAnimeVideoTask: %v", err)
	}

	run := func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := store.GetPublicTask(context.Background(), req.TaskID); err != nil {
					b.Error(err)
				}
			}
		})
	}
	b.Run("one_connection", func(b *testing.B) {
		store.db.SetMaxOpenConns(1)
		store.db.SetMaxIdleConns(1)
		run(b)
	})
	b.Run("four_connections", func(b *testing.B) {
		store.db.SetMaxOpenConns(4)
		store.db.SetMaxIdleConns(4)
		run(b)
	})
}
