package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type callbackAttempt struct {
	taskID      int64
	notifyURL   string
	attempts    int
	leasedUntil int64
}

func (s *Store) wakeCallbacks() {
	select {
	case s.callbackWake <- struct{}{}:
	default:
	}
}

func (s *Store) nextCallbackAttempt(ctx context.Context) (sql.NullInt64, error) {
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM workflow_callbacks
		WHERE next_attempt_at IS NOT NULL`).Scan(&next)
	return next, err
}

func (s *Store) claimCallbacks(ctx context.Context, now time.Time, limit int) ([]callbackAttempt, error) {
	lease := now.Add(callbackLeaseDuration).UnixMilli()
	// Moving the due time forward locks this batch until the sender records an
	// outcome or the lease expires. Concurrent dispatchers claim disjoint rows.
	rows, err := s.db.QueryContext(ctx, `UPDATE workflow_callbacks SET next_attempt_at = ?
		WHERE workflow_task_id IN (
			SELECT workflow_task_id FROM workflow_callbacks WHERE next_attempt_at <= ?
			ORDER BY next_attempt_at, workflow_task_id LIMIT ?
		) RETURNING workflow_task_id, notify_url, attempts`, lease, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []callbackAttempt
	for rows.Next() {
		attempt := callbackAttempt{leasedUntil: lease}
		if err := rows.Scan(&attempt.taskID, &attempt.notifyURL, &attempt.attempts); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

func (s *Store) failedStepResult(ctx context.Context, taskID int64) (json.RawMessage, error) {
	var result string
	err := s.db.QueryRowContext(ctx, `SELECT result_payload FROM workflow_steps
		WHERE workflow_task_id = ? AND status = ? AND result_payload <> ''
		ORDER BY step_index DESC LIMIT 1`, taskID, StatusFailed).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(result), err
}

func (s *Store) completeCallbackAttempt(ctx context.Context, attempt callbackAttempt, next, deliveredAt *int64, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_callbacks
		SET attempts = attempts + 1, next_attempt_at = ?, delivered_at = ?, last_error = ?
		WHERE workflow_task_id = ? AND next_attempt_at = ?`, next, deliveredAt, message, attempt.taskID, attempt.leasedUntil)
	return err
}
