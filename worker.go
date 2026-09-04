package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errBackendSubmissionUncertain = errors.New("backend submission acceptance is uncertain")

type recoverablePersistenceError struct {
	operation string
	err       error
}

type workflowStepFailure struct {
	index  int
	result json.RawMessage
	err    error
}

func (e *workflowStepFailure) Error() string {
	return e.err.Error()
}

func (e *workflowStepFailure) Unwrap() error {
	return e.err
}

func (e *recoverablePersistenceError) Error() string {
	return e.operation + ": " + e.err.Error()
}

func (e *recoverablePersistenceError) Unwrap() error {
	return e.err
}

func persistenceError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &recoverablePersistenceError{operation: operation, err: err}
}

type submissionVisibility uint8

const (
	submissionIndeterminate submissionVisibility = iota
	submissionFound
	submissionAbsent
)

type Worker struct {
	store                    *Store
	backend                  *BackendClient
	cfg                      Config
	queue                    chan int64
	seenMu                   sync.Mutex
	seen                     map[int64]struct{}
	wg                       sync.WaitGroup
	submissionRecoveryWindow time.Duration
}

func NewWorker(store *Store, backend *BackendClient, cfg Config) *Worker {
	normalizeConfig(&cfg)
	queueSize := cfg.WorkerQueueSize
	if queueSize < cfg.WorkerConcurrency {
		queueSize = cfg.WorkerConcurrency
	}
	recoveryWindow := cfg.HTTPTimeout + 12*time.Second
	if recoveryWindow < 12*time.Second {
		recoveryWindow = 12 * time.Second
	}
	return &Worker{
		store:                    store,
		backend:                  backend,
		cfg:                      cfg,
		queue:                    make(chan int64, queueSize),
		seen:                     make(map[int64]struct{}),
		submissionRecoveryWindow: recoveryWindow,
	}
}

func (w *Worker) Start(ctx context.Context) {
	w.wg.Add(w.cfg.WorkerConcurrency + 1)
	for i := 0; i < w.cfg.WorkerConcurrency; i++ {
		go func() {
			defer w.wg.Done()
			w.loop(ctx)
		}()
	}
	go func() {
		defer w.wg.Done()
		w.recoverLoop(ctx)
	}()
}

func (w *Worker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) QueueStats() (depth int, capacity int) {
	return len(w.queue), cap(w.queue)
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
		default:
		}
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
			w.markTaskFailed(id, err.Error())
		}
	}()
	return w.runTask(ctx, id)
}

func (w *Worker) recoverLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
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
	ctx, cancel := w.taskContext(parent, task.DeadlineAnchor, task.CreatedAt)
	defer cancel()
	if err := ctx.Err(); err != nil {
		timeoutErr := fmt.Errorf("workflow task %s exceeded its %s deadline: %w", task.TaskID, w.cfg.TaskTimeout, err)
		if parent.Err() == nil {
			w.markTaskFailed(id, timeoutErr.Error())
		}
		return timeoutErr
	}

	var req AnimeVideoRequest
	if err := json.Unmarshal(task.RequestPayload, &req); err != nil {
		w.markTaskFailed(id, "invalid stored request payload: "+err.Error())
		return err
	}
	req.APIKey = taskAPIKey(task.RequestPayload, req.APIKey)
	spec, videoScene, err := resolveBackendWorkflow(req)
	if err != nil {
		w.markTaskFailed(id, err.Error())
		return err
	}
	if err := w.store.MarkTaskRunning(ctx, id, StepAnimeImage); err != nil {
		return err
	}

	imageURL, err := w.ensureAnimeImage(ctx, task, req, spec)
	if err != nil {
		w.handleTaskError(parent, ctx, id, err)
		return err
	}
	if err := w.store.MarkTaskRunning(ctx, id, StepAnimeVideo); err != nil {
		return err
	}
	final, err := w.ensureAnimeVideo(ctx, task, req, spec, videoScene, imageURL)
	if err != nil {
		w.handleTaskError(parent, ctx, id, err)
		return err
	}
	return w.markTaskSuccess(id, final)
}

func (w *Worker) ensureAnimeImage(ctx context.Context, task *WorkflowTask, req AnimeVideoRequest, spec backendWorkflowSpec) (string, error) {
	step, err := w.store.GetStep(ctx, task.ID, 1)
	if err != nil {
		if !isNotFound(err) {
			err = persistenceError("load image workflow step", err)
		}
		return "", err
	}
	var result map[string]any
	if step.Status == StatusSuccess && len(step.ResultPayload) > 0 {
		_ = json.Unmarshal(step.ResultPayload, &result)
		if imageURL := extractURL(result); imageURL != "" {
			return imageURL, nil
		}
	}

	form := buildBackendImageForm(req, spec)
	rawReq, _ := json.Marshal(form)
	if err := w.store.UpdateStepStart(ctx, task.ID, 1, rawReq); err != nil {
		return "", persistenceError("persist image workflow step start", err)
	}

	backendID := step.BackendTaskID
	if backendID == "" && step.Status == StatusRunning {
		rawResp, resp, visibility := w.waitForBackendSubmissionVisibility(ctx, form, req.APIKey)
		if visibility == submissionFound {
			backendID = backendTaskID(resp)
			if err := w.updateStepAccepted(task.ID, 1, backendID, rawResp); err != nil {
				return "", err
			}
		} else if visibility == submissionIndeterminate {
			err := uncertainBackendSubmissionError(form["task_id"])
			_ = w.store.UpdateStepPollError(ctx, task.ID, 1, err.Error(), rawResp)
			return "", err
		}
	}
	if backendID == "" {
		rawResp, resp, err := w.postBackendForm(ctx, task.ID, 1, spec.ImagePath, form, req.APIKey)
		if err != nil {
			if !errors.Is(err, errBackendSubmissionUncertain) {
				err = w.failStep(1, err, rawResp)
			}
			return "", err
		}
		backendID = backendTaskID(resp)
		if backendID == "" {
			err := errors.New("backend image step did not return uuid/task_id")
			return "", w.failStep(1, err, rawResp)
		}
		if err := w.updateStepAccepted(task.ID, 1, backendID, rawResp); err != nil {
			return "", err
		}
	}

	rawResult, result, err := w.waitBackendTask(ctx, task.ID, 1, backendID, req.APIKey)
	if err != nil {
		return "", w.failStep(1, err, rawResult)
	}
	imageURL := extractURL(result)
	if imageURL == "" {
		err := fmt.Errorf("image step completed but no output URL was found in backend task %s", backendID)
		return "", w.failStep(1, err, rawResult)
	}
	if err := w.markStepSuccess(task.ID, 1, rawResult); err != nil {
		return "", err
	}
	return imageURL, nil
}

func buildBackendImageForm(req AnimeVideoRequest, spec backendWorkflowSpec) map[string]string {
	form := map[string]string{
		"source_path":     req.SourcePath,
		"scene_name":      req.SceneName,
		"incoming_prompt": defaultString(req.QwenIncomingPrompt, req.IncomingPrompt),
		"fee":             defaultString(req.Fee, "10"),
		"title":           req.Title,
		"is_encrypt":      "false",
		"is_watermark":    "false",
		"task_id":         backendStepTaskID(req.TaskID, "image"),
	}
	addBackendMetadata(
		form,
		req,
		spec.ImagePath != backendQwenTwoImagePath,
		spec.VideoPath != backendLTX8sVideoPath,
	)

	if spec.ImagePath == backendQwenTwoImagePath {
		form["target_path"] = req.TargetPath
	}
	return compactForm(form)
}

func buildBackendVideoForm(req AnimeVideoRequest, spec backendWorkflowSpec, videoScene string, imageURL string) map[string]string {
	form := map[string]string{
		"source_path":  imageURL,
		"scene_name":   videoScene,
		"fee":          defaultString(req.Fee, "10"),
		"title":        req.Title,
		"is_encrypt":   strconv.FormatBool(req.IsEncrypt),
		"is_watermark": strconv.FormatBool(watermarkEnabled(req.IsWatermark)),
		"video_format": defaultString(req.VideoFormat, "video/h264-mp4"),
		"task_id":      backendStepTaskID(req.TaskID, "video"),
	}
	addBackendMetadata(form, req, true, spec.VideoPath == backendLTX8sVideoPath)

	if spec.VideoPath == backendLTX8sVideoPath {
		form["incoming_prompt"] = defaultString(req.WanIncomingPrompt, req.IncomingPrompt)
		form["audio_enabled"] = strconv.FormatBool(audioEnabled(req.AudioEnabled))
	} else {
		form["output_format"] = defaultString(req.OutputFormat, "video")
		form["qwen_incoming_prompt"] = req.QwenIncomingPrompt
		form["wan_incoming_prompt"] = req.WanIncomingPrompt
	}
	return compactForm(form)
}

func addBackendMetadata(form map[string]string, req AnimeVideoRequest, includeHashKey bool, includeNotifyURL bool) {
	form["bid"] = req.BID
	form["app_id"] = req.AppID
	if includeHashKey {
		form["hash_key"] = req.HashKey
	}
	if includeNotifyURL {
		form["notify_url"] = req.NotifyURL
	}
}

func backendStepTaskID(workflowTaskID string, step string) string {
	workflowTaskID = strings.TrimSpace(workflowTaskID)
	if workflowTaskID == "" {
		return ""
	}
	return workflowTaskID + "_" + step
}

func audioEnabled(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func watermarkEnabled(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func (w *Worker) taskContext(parent context.Context, deadlineAnchor time.Time, createdAt time.Time) (context.Context, context.CancelFunc) {
	if deadlineAnchor.IsZero() {
		deadlineAnchor = createdAt
	}
	if deadlineAnchor.IsZero() {
		return context.WithTimeout(parent, w.cfg.TaskTimeout)
	}
	return context.WithDeadline(parent, deadlineAnchor.Add(w.cfg.TaskTimeout))
}

func (w *Worker) handleTaskError(parent context.Context, taskCtx context.Context, id int64, err error) {
	if parent.Err() != nil {
		return
	}
	var persistenceErr *recoverablePersistenceError
	if errors.As(err, &persistenceErr) && taskCtx.Err() == nil {
		return
	}
	if errors.Is(err, errBackendSubmissionUncertain) && taskCtx.Err() == nil {
		return
	}
	if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("workflow exceeded its %s deadline: %w", w.cfg.TaskTimeout, err)
	}
	var stepFailure *workflowStepFailure
	if errors.As(err, &stepFailure) {
		w.markTaskAndStepFailed(id, stepFailure.index, err.Error(), stepFailure.result)
		return
	}
	w.markTaskFailed(id, err.Error())
}

func (w *Worker) persistenceContext() (context.Context, context.CancelFunc) {
	timeout := w.cfg.HTTPTimeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (w *Worker) markTaskFailed(id int64, message string) {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	if err := w.store.MarkTaskFailed(ctx, id, message); err != nil {
		log.Printf("mark workflow task %d failed: %v", id, err)
	}
}

func (w *Worker) markTaskAndStepFailed(id int64, index int, message string, result json.RawMessage) {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	if err := w.store.MarkTaskAndStepFailed(ctx, id, index, message, result); err != nil {
		log.Printf("mark workflow task %d and step %d failed: %v", id, index, err)
	}
}

func (w *Worker) markTaskSuccess(id int64, result json.RawMessage) error {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	return w.store.MarkTaskSuccess(ctx, id, result)
}

func (w *Worker) updateStepAccepted(taskID int64, index int, backendTaskID string, response json.RawMessage) error {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	return persistenceError("persist accepted backend workflow step", w.store.UpdateStepAccepted(ctx, taskID, index, backendTaskID, response))
}

func (w *Worker) markStepSuccess(taskID int64, index int, result json.RawMessage) error {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	return persistenceError("persist successful backend workflow step", w.store.MarkStepSuccess(ctx, taskID, index, result))
}

func (w *Worker) failStep(index int, cause error, result json.RawMessage) error {
	return &workflowStepFailure{index: index, result: result, err: cause}
}

func (w *Worker) ensureAnimeVideo(ctx context.Context, task *WorkflowTask, req AnimeVideoRequest, spec backendWorkflowSpec, videoScene string, imageURL string) (json.RawMessage, error) {
	step, err := w.store.GetStep(ctx, task.ID, 2)
	if err != nil {
		if !isNotFound(err) {
			err = persistenceError("load video workflow step", err)
		}
		return nil, err
	}
	if step.Status == StatusSuccess && len(step.ResultPayload) > 0 {
		return step.ResultPayload, nil
	}
	form := buildBackendVideoForm(req, spec, videoScene, imageURL)
	rawReq, _ := json.Marshal(form)
	if err := w.store.UpdateStepStart(ctx, task.ID, 2, rawReq); err != nil {
		return nil, persistenceError("persist video workflow step start", err)
	}

	backendID := step.BackendTaskID
	if backendID == "" && step.Status == StatusRunning {
		rawResp, resp, visibility := w.waitForBackendSubmissionVisibility(ctx, form, req.APIKey)
		if visibility == submissionFound {
			backendID = backendTaskID(resp)
			if err := w.updateStepAccepted(task.ID, 2, backendID, rawResp); err != nil {
				return nil, err
			}
		} else if visibility == submissionIndeterminate {
			err := uncertainBackendSubmissionError(form["task_id"])
			_ = w.store.UpdateStepPollError(ctx, task.ID, 2, err.Error(), rawResp)
			return nil, err
		}
	}
	if backendID == "" {
		rawResp, resp, err := w.postBackendForm(ctx, task.ID, 2, spec.VideoPath, form, req.APIKey)
		if err != nil {
			if !errors.Is(err, errBackendSubmissionUncertain) {
				err = w.failStep(2, err, rawResp)
			}
			return nil, err
		}
		backendID = backendTaskID(resp)
		if backendID == "" {
			err := errors.New("backend video step did not return uuid/task_id")
			return nil, w.failStep(2, err, rawResp)
		}
		if err := w.updateStepAccepted(task.ID, 2, backendID, rawResp); err != nil {
			return nil, err
		}
	}

	rawResult, _, err := w.waitBackendTask(ctx, task.ID, 2, backendID, req.APIKey)
	if err != nil {
		return nil, w.failStep(2, err, rawResult)
	}
	if err := w.markStepSuccess(task.ID, 2, rawResult); err != nil {
		return nil, err
	}
	return rawResult, nil
}

func (w *Worker) postBackendForm(ctx context.Context, workflowTaskID int64, stepIndex int, path string, form map[string]string, apiKey string) (json.RawMessage, map[string]any, error) {
	attempts := w.cfg.MaxSubmitRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastRaw json.RawMessage
	var lastResp map[string]any
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		raw, resp, err := w.backend.PostForm(ctx, path, form, apiKey)
		if err == nil {
			return raw, resp, nil
		}
		lastRaw = raw
		lastResp = resp
		lastErr = err
		if !isTransientBackendError(err) {
			return lastRaw, lastResp, lastErr
		}

		if isAmbiguousSubmitError(err) {
			_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, fmt.Sprintf("backend submission returned an ambiguous response; checking task_id visibility: %s", err.Error()), raw)
			acceptedRaw, acceptedResp, visibility := w.waitForBackendSubmissionVisibility(ctx, form, apiKey)
			switch visibility {
			case submissionFound:
				return acceptedRaw, acceptedResp, nil
			case submissionIndeterminate:
				return lastRaw, lastResp, uncertainBackendSubmissionError(form["task_id"])
			case submissionAbsent:
				if attempt == attempts {
					return lastRaw, lastResp, lastErr
				}
				_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, fmt.Sprintf("backend submission was not visible after the recovery window; retrying submit %d/%d: %s", attempt, attempts-1, err.Error()), raw)
				continue
			}
		}

		if attempt == attempts {
			return lastRaw, lastResp, lastErr
		}
		delay := submitRetryDelay(attempt)
		_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, fmt.Sprintf("submit backend step failed, retry %d/%d after %s: %s", attempt, attempts-1, delay, err.Error()), raw)
		select {
		case <-ctx.Done():
			return lastRaw, lastResp, ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastRaw, lastResp, lastErr
}

func (w *Worker) waitForBackendSubmissionVisibility(ctx context.Context, form map[string]string, apiKey string) (json.RawMessage, map[string]any, submissionVisibility) {
	taskID := strings.TrimSpace(form["task_id"])
	if taskID == "" {
		return nil, nil, submissionIndeterminate
	}

	firstRaw, firstResp, firstVisibility := w.lookupBackendSubmission(ctx, taskID, apiKey)
	if firstVisibility == submissionFound {
		return firstRaw, firstResp, firstVisibility
	}

	window := w.submissionRecoveryWindow
	if window <= 0 {
		window = 12 * time.Second
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return firstRaw, firstResp, submissionIndeterminate
	case <-timer.C:
	}
	finalRaw, finalResp, finalVisibility := w.lookupBackendSubmission(ctx, taskID, apiKey)
	if finalVisibility == submissionFound {
		return finalRaw, finalResp, finalVisibility
	}
	if firstVisibility == submissionAbsent && finalVisibility == submissionAbsent {
		return finalRaw, finalResp, submissionAbsent
	}
	return finalRaw, finalResp, submissionIndeterminate
}

func (w *Worker) lookupBackendSubmission(ctx context.Context, taskID string, apiKey string) (json.RawMessage, map[string]any, submissionVisibility) {
	raw, resp, err := w.backend.GetTask(ctx, taskID, apiKey)
	if err == nil {
		if backendTaskID(resp) != "" {
			return raw, resp, submissionFound
		}
		return raw, resp, submissionIndeterminate
	}
	if isBackendHTTPStatus(err, http.StatusNotFound) {
		return raw, resp, submissionAbsent
	}
	return raw, resp, submissionIndeterminate
}

func uncertainBackendSubmissionError(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("%w: no task_id is available for verification", errBackendSubmissionUncertain)
	}
	return fmt.Errorf("%w for task_id %q", errBackendSubmissionUncertain, taskID)
}

func isBackendHTTPStatus(err error, status int) bool {
	var backendErr *BackendHTTPError
	return errors.As(err, &backendErr) && backendErr.StatusCode == status
}

func isTransientBackendError(err error) bool {
	if isAmbiguousSubmitError(err) {
		return true
	}
	var backendErr *BackendHTTPError
	return errors.As(err, &backendErr) && backendErr.StatusCode == http.StatusTooManyRequests
}

func isAmbiguousSubmitError(err error) bool {
	if errors.Is(err, errBackendSubmitOutcomeUnknown) {
		return true
	}
	var backendErr *BackendHTTPError
	return errors.As(err, &backendErr) && backendErr.StatusCode >= 500
}

func submitRetryDelay(attempt int) time.Duration {
	delay := time.Duration(attempt*attempt) * time.Second
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (w *Worker) waitBackendTask(ctx context.Context, workflowTaskID int64, stepIndex int, backendTaskID string, apiKey string) (json.RawMessage, map[string]any, error) {
	var lastRaw json.RawMessage
	consecutiveErrors := 0
	taskNotFoundErrors := 0
	for {
		raw, result, err := w.backend.GetTask(ctx, backendTaskID, apiKey)
		if err != nil {
			lastRaw = raw
			if isBackendHTTPStatus(err, http.StatusNotFound) {
				taskNotFoundErrors++
				_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, fmt.Sprintf("backend task not visible yet (%d/%d): %s", taskNotFoundErrors, w.cfg.MaxTaskNotFound, err.Error()), raw)
				if taskNotFoundErrors >= w.cfg.MaxTaskNotFound {
					return lastRaw, nil, fmt.Errorf("backend task %s not found after %d queries: %w", backendTaskID, taskNotFoundErrors, err)
				}
				select {
				case <-ctx.Done():
					return w.finalBackendCheck(backendTaskID, apiKey, lastRaw, nil, ctx.Err())
				case <-time.After(w.cfg.PollInterval):
					continue
				}
			}
			consecutiveErrors++
			_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, "poll backend task failed: "+err.Error(), raw)
			if consecutiveErrors >= w.cfg.MaxPollErrors {
				return lastRaw, nil, fmt.Errorf("backend task %s query failed %d times: %w", backendTaskID, consecutiveErrors, err)
			}
			select {
			case <-ctx.Done():
				return w.finalBackendCheck(backendTaskID, apiKey, lastRaw, nil, ctx.Err())
			case <-time.After(w.cfg.PollInterval):
				continue
			}
		}
		taskNotFoundErrors = 0
		lastRaw = raw
		status, valid := parseBackendStatus(result)
		if !valid {
			consecutiveErrors++
			message := fmt.Sprintf("backend task %s returned a missing or unknown status (%d/%d)", backendTaskID, consecutiveErrors, w.cfg.MaxPollErrors)
			_ = w.store.UpdateStepPollError(ctx, workflowTaskID, stepIndex, message, raw)
			if consecutiveErrors >= w.cfg.MaxPollErrors {
				return lastRaw, result, errors.New(message)
			}
			select {
			case <-ctx.Done():
				return w.finalBackendCheck(backendTaskID, apiKey, lastRaw, result, ctx.Err())
			case <-time.After(w.cfg.PollInterval):
				continue
			}
		}
		consecutiveErrors = 0
		switch status {
		case StatusSuccess:
			return raw, result, nil
		case StatusFailed:
			return raw, result, fmt.Errorf("backend task %s failed with status -1", backendTaskID)
		default:
			select {
			case <-ctx.Done():
				return w.finalBackendCheck(backendTaskID, apiKey, lastRaw, result, ctx.Err())
			case <-time.After(w.cfg.PollInterval):
			}
		}
	}
}

func (w *Worker) finalBackendCheck(backendTaskID string, apiKey string, lastRaw json.RawMessage, lastResult map[string]any, cause error) (json.RawMessage, map[string]any, error) {
	if errors.Is(cause, context.Canceled) {
		return lastRaw, lastResult, fmt.Errorf("stopped waiting for backend task %s: %w", backendTaskID, cause)
	}
	timeout := w.cfg.HTTPTimeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, result, err := w.backend.GetTask(ctx, backendTaskID, apiKey)
	if err == nil {
		status, valid := parseBackendStatus(result)
		if !valid {
			return raw, result, fmt.Errorf("backend task %s returned a missing or unknown status at deadline: %w", backendTaskID, cause)
		}
		switch status {
		case StatusSuccess:
			return raw, result, nil
		case StatusFailed:
			return raw, result, fmt.Errorf("backend task %s failed with status -1", backendTaskID)
		default:
			return raw, result, fmt.Errorf("timeout waiting for backend task %s: %w", backendTaskID, cause)
		}
	}
	if lastResult != nil && backendStatus(lastResult) == StatusSuccess {
		return lastRaw, lastResult, nil
	}
	return lastRaw, lastResult, fmt.Errorf("timeout waiting for backend task %s: %w", backendTaskID, cause)
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
		"download_url", "image_url", "file_url", "url", "video_url", "intermediate_image_url",
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
		for _, key := range []string{"download_url", "source_path", "image_url", "file_url", "url", "video_url", "filename"} {
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
