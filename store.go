package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS workflow_tasks (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_tasks_status_updated ON workflow_tasks(status, updated_at)`,
		`CREATE TABLE IF NOT EXISTS workflow_steps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_task_id INTEGER NOT NULL,
			step_index INTEGER NOT NULL,
			step_name TEXT NOT NULL,
			status INTEGER NOT NULL,
			backend_task_id TEXT NOT NULL DEFAULT '',
			request_payload TEXT,
			response_payload TEXT,
			result_payload TEXT,
			error_message TEXT NOT NULL DEFAULT '',
			started_at DATETIME,
			finished_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(workflow_task_id, step_index),
			FOREIGN KEY(workflow_task_id) REFERENCES workflow_tasks(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_steps_backend_task ON workflow_steps(backend_task_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateAnimeVideoTask(ctx context.Context, taskID string, req AnimeVideoRequest, raw json.RawMessage) (*WorkflowTask, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `INSERT INTO workflow_tasks
		(task_id, workflow_type, status, current_step, request_payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		taskID, WorkflowAnimeUndressVideo, StatusPending, StepAnimeImage, string(raw), now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	steps := []struct {
		index int
		name  string
	}{
		{1, StepAnimeImage},
		{2, StepAnimeVideo},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_steps
			(workflow_task_id, step_index, step_name, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, step.index, step.name, StatusPending, now, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &WorkflowTask{
		ID: id, TaskID: taskID, WorkflowType: WorkflowAnimeUndressVideo, Status: StatusPending,
		CurrentStep: StepAnimeImage, RequestPayload: raw, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Store) GetTaskDetail(ctx context.Context, taskID string) (*TaskDetail, error) {
	task, err := s.getTaskByQuery(ctx, `SELECT id, task_id, workflow_type, status, current_step, request_payload,
		COALESCE(final_result, ''), error_message, created_at, updated_at, finished_at
		FROM workflow_tasks WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	steps, err := s.ListSteps(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return &TaskDetail{WorkflowTask: *task, Steps: steps}, nil
}

func (s *Store) GetTaskByID(ctx context.Context, id int64) (*WorkflowTask, error) {
	return s.getTaskByQuery(ctx, `SELECT id, task_id, workflow_type, status, current_step, request_payload,
		COALESCE(final_result, ''), error_message, created_at, updated_at, finished_at
		FROM workflow_tasks WHERE id = ?`, id)
}

func (s *Store) getTaskByQuery(ctx context.Context, query string, arg any) (*WorkflowTask, error) {
	var task WorkflowTask
	var requestPayload string
	var finalResult string
	var finishedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, query, arg).Scan(
		&task.ID, &task.TaskID, &task.WorkflowType, &task.Status, &task.CurrentStep,
		&requestPayload, &finalResult, &task.ErrorMessage, &task.CreatedAt, &task.UpdatedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}
	task.RequestPayload = json.RawMessage(requestPayload)
	if finalResult != "" {
		task.FinalResult = json.RawMessage(finalResult)
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	return &task, nil
}

func (s *Store) ListSteps(ctx context.Context, taskID int64) ([]WorkflowStepRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workflow_task_id, step_index, step_name, status,
		backend_task_id, COALESCE(request_payload, ''), COALESCE(response_payload, ''), COALESCE(result_payload, ''),
		error_message, started_at, finished_at, created_at, updated_at
		FROM workflow_steps WHERE workflow_task_id = ? ORDER BY step_index`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []WorkflowStepRecord
	for rows.Next() {
		var step WorkflowStepRecord
		var requestPayload, responsePayload, resultPayload string
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&step.ID, &step.WorkflowTaskID, &step.StepIndex, &step.StepName, &step.Status,
			&step.BackendTaskID, &requestPayload, &responsePayload, &resultPayload, &step.ErrorMessage,
			&startedAt, &finishedAt, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return nil, err
		}
		if requestPayload != "" {
			step.RequestPayload = json.RawMessage(requestPayload)
		}
		if responsePayload != "" {
			step.ResponsePayload = json.RawMessage(responsePayload)
		}
		if resultPayload != "" {
			step.ResultPayload = json.RawMessage(resultPayload)
		}
		if startedAt.Valid {
			step.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			step.FinishedAt = &finishedAt.Time
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s *Store) CountTasks(ctx context.Context) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_tasks`).Scan(&total)
	return total, err
}

func (s *Store) ListTasks(ctx context.Context, limit int, offset int) ([]WorkflowTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, workflow_type, status, current_step, request_payload,
		COALESCE(final_result, ''), error_message, created_at, updated_at, finished_at
		FROM workflow_tasks ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkflowTask
	for rows.Next() {
		var task WorkflowTask
		var requestPayload, finalResult string
		var finishedAt sql.NullTime
		if err := rows.Scan(&task.ID, &task.TaskID, &task.WorkflowType, &task.Status, &task.CurrentStep,
			&requestPayload, &finalResult, &task.ErrorMessage, &task.CreatedAt, &task.UpdatedAt, &finishedAt); err != nil {
			return nil, err
		}
		task.RequestPayload = json.RawMessage(requestPayload)
		if finalResult != "" {
			task.FinalResult = json.RawMessage(finalResult)
		}
		if finishedAt.Valid {
			task.FinishedAt = &finishedAt.Time
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) ListRunnableTaskIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM workflow_tasks
		WHERE status IN (?, ?) ORDER BY id ASC LIMIT ?`, StatusPending, StatusRunning, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) MarkTaskRunning(ctx context.Context, taskID int64, currentStep string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_tasks SET status = ?, current_step = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		StatusRunning, currentStep, now, taskID, StatusPending, StatusRunning)
	return err
}

func (s *Store) MarkTaskSuccess(ctx context.Context, taskID int64, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_tasks
		SET status = ?, final_result = ?, error_message = '', updated_at = ?, finished_at = ?
		WHERE id = ?`, StatusSuccess, string(result), now, now, taskID)
	return err
}

func (s *Store) MarkTaskFailed(ctx context.Context, taskID int64, message string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_tasks
		SET status = ?, error_message = ?, updated_at = ?, finished_at = ?
		WHERE id = ?`, StatusFailed, truncateMessage(message, 4000), now, now, taskID)
	return err
}

func (s *Store) UpdateStepStart(ctx context.Context, taskID int64, index int, request json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_steps
		SET status = ?, request_payload = ?, error_message = '', started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE workflow_task_id = ? AND step_index = ?`,
		StatusRunning, string(request), now, now, taskID, index)
	return err
}

func (s *Store) UpdateStepAccepted(ctx context.Context, taskID int64, index int, backendTaskID string, response json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_steps
		SET status = ?, backend_task_id = ?, response_payload = ?, updated_at = ?
		WHERE workflow_task_id = ? AND step_index = ?`,
		StatusRunning, backendTaskID, string(response), now, taskID, index)
	return err
}

func (s *Store) UpdateStepPollError(ctx context.Context, taskID int64, index int, message string, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_steps
		SET error_message = ?, result_payload = ?, updated_at = ?
		WHERE workflow_task_id = ? AND step_index = ? AND status = ?`,
		truncateMessage(message, 4000), string(result), now, taskID, index, StatusRunning)
	return err
}

func (s *Store) MarkStepSuccess(ctx context.Context, taskID int64, index int, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_steps
		SET status = ?, result_payload = ?, error_message = '', updated_at = ?, finished_at = ?
		WHERE workflow_task_id = ? AND step_index = ?`,
		StatusSuccess, string(result), now, now, taskID, index)
	return err
}

func (s *Store) MarkStepFailed(ctx context.Context, taskID int64, index int, message string, result json.RawMessage) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_steps
		SET status = ?, result_payload = ?, error_message = ?, updated_at = ?, finished_at = ?
		WHERE workflow_task_id = ? AND step_index = ?`,
		StatusFailed, string(result), truncateMessage(message, 4000), now, now, taskID, index)
	return err
}

func (s *Store) GetStep(ctx context.Context, taskID int64, index int) (*WorkflowStepRecord, error) {
	steps, err := s.ListSteps(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		if step.StepIndex == index {
			return &step, nil
		}
	}
	return nil, fmt.Errorf("step %d not found", index)
}

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
