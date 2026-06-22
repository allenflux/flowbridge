package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	store   *Store
	backend *BackendClient
	cfg     Config
	queue   chan int64
	seenMu  sync.Mutex
	seen    map[int64]struct{}
}

func NewWorker(store *Store, backend *BackendClient, cfg Config) *Worker {
	queueSize := cfg.WorkerQueueSize
	if queueSize < cfg.WorkerConcurrency {
		queueSize = cfg.WorkerConcurrency
	}
	return &Worker{
		store:   store,
		backend: backend,
		cfg:     cfg,
		queue:   make(chan int64, queueSize),
		seen:    make(map[int64]struct{}),
	}
}

func (w *Worker) Start(ctx context.Context) {
	for i := 0; i < w.cfg.WorkerConcurrency; i++ {
		go w.loop(ctx)
	}
	go w.recoverLoop(ctx)
}

func (w *Worker) Enqueue(id int64) bool {
	w.seenMu.Lock()
	if _, ok := w.seen[id]; ok {
		w.seenMu.Unlock()
		return true
	}
	w.seen[id] = struct{}{}
	w.seenMu.Unlock()

	select {
	case w.queue <- id:
		return true
	default:
		w.done(id)
		return false
	}
}

func (w *Worker) done(id int64) {
	w.seenMu.Lock()
	delete(w.seen, id)
	w.seenMu.Unlock()
}

func (w *Worker) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-w.queue:
			if err := w.safeRunTask(ctx, id); err != nil {
				log.Printf("workflow task %d failed: %v", id, err)
			}
			w.done(id)
		}
	}
}

func (w *Worker) safeRunTask(ctx context.Context, id int64) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("workflow task panic: %v", recovered)
			log.Printf("panic in workflow task %d: %v\n%s", id, recovered, debug.Stack())
			_ = w.store.MarkTaskFailed(context.Background(), id, truncateMessage(err.Error(), 2000))
		}
	}()
	return w.runTask(ctx, id)
}

func (w *Worker) recoverLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		ids, err := w.store.ListRunnableTaskIDs(ctx, w.cfg.WorkerConcurrency*4)
		if err == nil {
			for _, id := range ids {
				w.Enqueue(id)
			}
		} else {
			log.Printf("recover runnable tasks failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) runTask(parent context.Context, id int64) error {
	task, err := w.store.GetTaskByID(parent, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if task.Status == StatusSuccess || task.Status == StatusFailed {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, w.cfg.TaskTimeout)
	defer cancel()

	var req AnimeVideoRequest
	if err := json.Unmarshal(task.RequestPayload, &req); err != nil {
		_ = w.store.MarkTaskFailed(parent, id, "invalid stored request payload: "+err.Error())
		return err
	}
	req.APIKey = taskAPIKey(task.RequestPayload, req.APIKey)
	if err := w.store.MarkTaskRunning(ctx, id, StepAnimeImage); err != nil {
		return err
	}

	imageURL, err := w.ensureAnimeImage(ctx, task, req)
	if err != nil {
		_ = w.store.MarkTaskFailed(parent, id, err.Error())
		return err
	}
	if err := w.store.MarkTaskRunning(ctx, id, StepAnimeVideo); err != nil {
		return err
	}
	final, err := w.ensureAnimeVideo(ctx, task, req, imageURL)
	if err != nil {
		_ = w.store.MarkTaskFailed(parent, id, err.Error())
		return err
	}
	return w.store.MarkTaskSuccess(ctx, id, final)
}

func (w *Worker) ensureAnimeImage(ctx context.Context, task *WorkflowTask, req AnimeVideoRequest) (string, error) {
	step, err := w.store.GetStep(ctx, task.ID, 1)
	if err != nil {
		return "", err
	}
	var result map[string]any
	if step.Status == StatusSuccess && len(step.ResultPayload) > 0 {
		_ = json.Unmarshal(step.ResultPayload, &result)
		if imageURL := extractURL(result); imageURL != "" {
			return imageURL, nil
		}
	}

	form := map[string]string{
		"source_path":     req.SourcePath,
		"scene_name":      req.SceneName,
		"incoming_prompt": defaultString(req.QwenIncomingPrompt, req.IncomingPrompt),
		"fee":             defaultString(req.Fee, "10"),
		"title":           req.Title,
		"is_encrypt":      "false",
	}
	if req.BID != "" {
		form["bid"] = req.BID
	}
	if req.AppID != "" {
		form["app_id"] = req.AppID
	}
	if req.HashKey != "" {
		form["hash_key"] = req.HashKey
	}
	if req.NotifyURL != "" {
		form["notify_url"] = req.NotifyURL
	}
	form = compactForm(form)
	rawReq, _ := json.Marshal(form)
	if err := w.store.UpdateStepStart(ctx, task.ID, 1, rawReq); err != nil {
		return "", err
	}

	backendID := step.BackendTaskID
	alternateBackendID := backendUUIDFromRaw(step.ResponsePayload)
	if backendID == "" {
		rawResp, resp, err := w.backend.PostForm(ctx, "/api/public/generate/undress/anime", form, req.APIKey)
		if err != nil {
			_ = w.store.MarkStepFailed(ctx, task.ID, 1, err.Error(), rawResp)
			return "", err
		}
		backendID = backendTaskID(resp)
		alternateBackendID = backendUUID(resp)
		if backendID == "" {
			err := errors.New("backend image step did not return uuid/task_id")
			_ = w.store.MarkStepFailed(ctx, task.ID, 1, err.Error(), rawResp)
			return "", err
		}
		if err := w.store.UpdateStepAccepted(ctx, task.ID, 1, backendID, rawResp); err != nil {
			return "", err
		}
	}

	rawResult, result, err := w.waitBackendTask(ctx, task.ID, 1, backendID, alternateBackendID, req.APIKey)
	if err != nil {
		_ = w.store.MarkStepFailed(ctx, task.ID, 1, err.Error(), rawResult)
		return "", err
	}
	imageURL := extractURL(result)
	if imageURL == "" {
		err := fmt.Errorf("image step completed but no output URL was found in backend task %s", backendID)
		_ = w.store.MarkStepFailed(ctx, task.ID, 1, err.Error(), rawResult)
		return "", err
	}
	if err := w.store.MarkStepSuccess(ctx, task.ID, 1, rawResult); err != nil {
		return "", err
	}
	return imageURL, nil
}

func (w *Worker) ensureAnimeVideo(ctx context.Context, task *WorkflowTask, req AnimeVideoRequest, imageURL string) (json.RawMessage, error) {
	step, err := w.store.GetStep(ctx, task.ID, 2)
	if err != nil {
		return nil, err
	}
	if step.Status == StatusSuccess && len(step.ResultPayload) > 0 {
		return step.ResultPayload, nil
	}
	form := map[string]string{
		"source_path":   imageURL,
		"scene_name":    defaultString(req.VideoSceneName, req.SceneName),
		"output_format": defaultString(req.OutputFormat, "video"),
		"fee":           defaultString(req.Fee, "10"),
		"title":         req.Title,
	}
	if req.QwenIncomingPrompt != "" {
		form["qwen_incoming_prompt"] = req.QwenIncomingPrompt
	}
	if req.WanIncomingPrompt != "" {
		form["wan_incoming_prompt"] = req.WanIncomingPrompt
	}
	form = compactForm(form)
	rawReq, _ := json.Marshal(form)
	if err := w.store.UpdateStepStart(ctx, task.ID, 2, rawReq); err != nil {
		return nil, err
	}

	backendID := step.BackendTaskID
	alternateBackendID := backendUUIDFromRaw(step.ResponsePayload)
	if backendID == "" {
		rawResp, resp, err := w.backend.PostForm(ctx, "/api/public/generate/undress/anime/video", form, req.APIKey)
		if err != nil {
			_ = w.store.MarkStepFailed(ctx, task.ID, 2, err.Error(), rawResp)
			return nil, err
		}
		backendID = backendTaskID(resp)
		alternateBackendID = backendUUID(resp)
		if backendID == "" {
			err := errors.New("backend video step did not return uuid/task_id")
			_ = w.store.MarkStepFailed(ctx, task.ID, 2, err.Error(), rawResp)
			return nil, err
		}
		if err := w.store.UpdateStepAccepted(ctx, task.ID, 2, backendID, rawResp); err != nil {
			return nil, err
		}
	}

	rawResult, _, err := w.waitBackendTask(ctx, task.ID, 2, backendID, alternateBackendID, req.APIKey)
	if err != nil {
		_ = w.store.MarkStepFailed(ctx, task.ID, 2, err.Error(), rawResult)
		return nil, err
	}
	if err := w.store.MarkStepSuccess(ctx, task.ID, 2, rawResult); err != nil {
		return nil, err
	}
	return rawResult, nil
}

func (w *Worker) waitBackendTask(ctx context.Context, workflowTaskID int64, stepIndex int, backendTaskID string, alternateBackendTaskID string, apiKey string) (json.RawMessage, map[string]any, error) {
	var lastRaw json.RawMessage
	consecutiveErrors := 0
	activeTaskID := backendTaskID
	fallbackTried := false
	for {
		raw, result, err := w.backend.GetTask(ctx, activeTaskID, apiKey)
		if err != nil {
			lastRaw = raw
			consecutiveErrors++
			if strings.Contains(err.Error(), "HTTP 404") && !fallbackTried && alternateBackendTaskID != "" && alternateBackendTaskID != activeTaskID {
				fallbackTried = true
				_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, "backend task_id returned 404, retrying with backend uuid: "+err.Error(), raw)
				activeTaskID = alternateBackendTaskID
				continue
			}
			_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, "poll backend task failed: "+err.Error(), raw)
			if consecutiveErrors >= w.cfg.MaxPollErrors {
				return lastRaw, nil, fmt.Errorf("backend task %s query failed %d times: %w", activeTaskID, consecutiveErrors, err)
			}
			select {
			case <-ctx.Done():
				return lastRaw, nil, fmt.Errorf("timeout waiting for backend task %s: %w", activeTaskID, ctx.Err())
			case <-time.After(w.cfg.PollInterval):
				continue
			}
		}
		consecutiveErrors = 0
		lastRaw = raw
		switch status := backendStatus(result); status {
		case StatusSuccess:
			return raw, result, nil
		case StatusFailed:
			return raw, result, fmt.Errorf("backend task %s failed with status -1", activeTaskID)
		default:
			select {
			case <-ctx.Done():
				return lastRaw, result, fmt.Errorf("timeout waiting for backend task %s: %w", activeTaskID, ctx.Err())
			case <-time.After(w.cfg.PollInterval):
			}
		}
	}
}

func backendUUIDFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return ""
	}
	return backendUUID(decoded)
}

func taskAPIKey(raw json.RawMessage, fallback string) string {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return fallback
	}
	for _, key := range []string{"apikey", "Apikey", "api_key"} {
		if value, ok := payload[key].(string); ok && !strings.Contains(value, "****") {
			return value
		}
	}
	return fallback
}

func extractURL(payload map[string]any) string {
	for _, key := range []string{"out_data", "data", "result", "output", "outputs"} {
		if value, ok := payload[key]; ok {
			if found := extractURLValue(value); found != "" {
				return found
			}
		}
	}
	preferred := []string{
		"image_url", "file_url", "url", "video_url", "intermediate_image_url",
	}
	for _, key := range preferred {
		if value, ok := payload[key].(string); ok && isLikelyURL(value) {
			return value
		}
	}
	return extractURLValue(payload)
}

func extractURLValue(value any) string {
	switch typed := value.(type) {
	case string:
		candidate := strings.TrimSpace(typed)
		if strings.HasPrefix(candidate, "{") || strings.HasPrefix(candidate, "[") {
			var decoded any
			if json.Unmarshal([]byte(candidate), &decoded) == nil {
				return extractURLValue(decoded)
			}
		}
		if isLikelyURL(candidate) {
			return candidate
		}
	case []any:
		for _, item := range typed {
			if found := extractURLValue(item); found != "" {
				return found
			}
		}
	case map[string]any:
		for _, key := range []string{"source_path", "image_url", "file_url", "url", "video_url", "filename"} {
			if found := extractURLValue(typed[key]); found != "" {
				return found
			}
		}
		for _, item := range typed {
			if found := extractURLValue(item); found != "" {
				return found
			}
		}
	}
	return ""
}

func isLikelyURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func compactForm(form map[string]string) map[string]string {
	compacted := make(map[string]string, len(form))
	for key, value := range form {
		if strings.TrimSpace(value) != "" {
			compacted[key] = value
		}
	}
	return compacted
}

func truncateMessage(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "... truncated"
}
