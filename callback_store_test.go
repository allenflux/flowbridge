package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func createCallbackTestTask(t *testing.T, store *Store, taskID, notifyURL string) *WorkflowTask {
	t.Helper()
	req := AnimeVideoRequest{TaskID: taskID, NotifyURL: notifyURL}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateAnimeVideoTask(context.Background(), taskID, req, raw)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCallbackOutboxRecoversExpiredLeaseAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createCallbackTestTask(t, store, "with-callback", "https://callback.example/notify")
	noCallback := createCallbackTestTask(t, store, "without-callback", "")
	when, err := store.nextCallbackAttempt(ctx)
	if err != nil || when.Valid {
		t.Fatalf("pending tasks queued a callback: %v, %v", when, err)
	}
	result := json.RawMessage(`{"status":2,"out_data":["https://cdn.example/final.mp4"]}`)
	if err := store.MarkTaskSuccess(ctx, task.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTaskFailed(ctx, task.ID, "duplicate finalization"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTaskFailed(ctx, noCallback.ID, "failed"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Add(time.Second)
	initial, err := store.claimCallbacks(ctx, now, 8)
	if err != nil || len(initial) != 1 || initial[0].taskID != task.ID {
		t.Fatalf("claims = %+v, err = %v, want one for %d", initial, err, task.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	inFlight, err := reopened.claimCallbacks(ctx, now, 8)
	if err != nil || len(inFlight) != 0 {
		t.Fatalf("reclaimed active lease: %+v, %v", inFlight, err)
	}
	reclaimed, err := reopened.claimCallbacks(ctx, now.Add(callbackLeaseDuration), 8)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].taskID != task.ID {
		t.Fatalf("expired lease was not recovered: %+v, %v", reclaimed, err)
	}
	stamp := now.UnixMilli()
	if err := reopened.completeCallbackAttempt(ctx, initial[0], nil, &stamp, ""); err != nil {
		t.Fatal(err)
	}
	when, err = reopened.nextCallbackAttempt(ctx)
	if err != nil || !when.Valid || when.Int64 != reclaimed[0].leasedUntil {
		t.Fatalf("stale sender changed current lease: %v, %v", when, err)
	}
	if err := reopened.completeCallbackAttempt(ctx, reclaimed[0], nil, &stamp, ""); err != nil {
		t.Fatal(err)
	}
	when, err = reopened.nextCallbackAttempt(ctx)
	if err != nil || when.Valid {
		t.Fatalf("delivery still scheduled: %v, %v", when, err)
	}
}

func TestCallbackDispatcherPersistsBackoffAndTerminalOutcome(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := createCallbackTestTask(t, store, "retry-callback", "https://callback.example/notify")
	if err := store.MarkTaskFailed(ctx, task.ID, "workflow failed"); err != nil {
		t.Fatal(err)
	}
	dispatcher := newCallbackDispatcher(store, 2)
	status := http.StatusTooManyRequests
	dispatcher.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	})
	first, err := store.claimCallbacks(ctx, time.Now().Add(time.Second), 2)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %+v, err = %v", first, err)
	}
	dispatcher.deliver(ctx, first[0])
	next, err := store.nextCallbackAttempt(ctx)
	if err != nil || !next.Valid || time.Until(time.UnixMilli(next.Int64)) < 4*time.Second {
		t.Fatalf("retry not delayed: %v, %v", next, err)
	}
	second, err := store.claimCallbacks(ctx, time.UnixMilli(next.Int64), 2)
	if err != nil || len(second) != 1 || second[0].attempts != 1 {
		t.Fatalf("second claim = %+v, err = %v", second, err)
	}
	status = http.StatusNoContent
	dispatcher.deliver(ctx, second[0])
	var attempts int
	var due, delivered sql.NullInt64
	err = store.db.QueryRowContext(ctx, `SELECT attempts, next_attempt_at, delivered_at FROM workflow_callbacks
		WHERE workflow_task_id = ?`, task.ID).Scan(&attempts, &due, &delivered)
	if err != nil || attempts != 2 || due.Valid || !delivered.Valid {
		t.Fatalf("delivery attempts = %d, due = %v, delivered = %v, err = %v", attempts, due, delivered, err)
	}
}

func TestCallbackDispatcherBoundsConcurrentRequests(t *testing.T) {
	const concurrency, total = 3, 12
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := OpenStore(filepath.Join(t.TempDir(), "flowbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := range total {
		task := createCallbackTestTask(t, store, fmt.Sprint(i), "https://callback.example/notify")
		if err := store.MarkTaskFailed(ctx, task.ID, "workflow failed"); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher := newCallbackDispatcher(store, concurrency)
	started := make(chan struct{}, total)
	release := make(chan struct{})
	dispatcher.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatcher.run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	for range concurrency {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("callback concurrency never filled")
		}
	}
	select {
	case <-started:
		t.Fatal("callback concurrency exceeded its limit")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for i := concurrency; i < total; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("dispatched %d of %d callbacks", i, total)
		}
	}
}
