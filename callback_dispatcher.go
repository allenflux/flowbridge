package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	callbackHTTPTimeout   = 10 * time.Second
	callbackLeaseDuration = 30 * time.Second
	callbackRecheck       = 5 * time.Second
	callbackMaxAttempts   = 6
)

type callbackDispatcher struct {
	store       *Store
	client      *http.Client
	concurrency int
}

func newCallbackDispatcher(store *Store, concurrency int) *callbackDispatcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2 * concurrency
	transport.MaxIdleConnsPerHost = concurrency
	return &callbackDispatcher{
		store:       store,
		concurrency: concurrency,
		client: &http.Client{
			Timeout:   callbackHTTPTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (d *callbackDispatcher) run(ctx context.Context) {
	defer d.client.CloseIdleConnections()
	timer := time.NewTimer(0)
	defer timer.Stop()
	finished := make(chan struct{}, d.concurrency)
	active := 0
	defer func() {
		for ; active > 0; active-- {
			<-finished
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.store.callbackWake:
		case <-timer.C:
		case <-finished:
			active--
		}
		if ctx.Err() != nil {
			return
		}
	drainFinished:
		for active > 0 {
			select {
			case <-finished:
				active--
			default:
				break drainFinished
			}
		}
		if active == d.concurrency {
			continue
		}
		attempts, delay := d.takeDue(ctx, d.concurrency-active)
		active += len(attempts)
		for _, attempt := range attempts {
			go func() {
				defer func() { finished <- struct{}{} }()
				d.deliver(ctx, attempt)
			}()
		}
		if active == d.concurrency {
			timer.Stop()
		} else {
			timer.Reset(delay)
		}
	}
}

func (d *callbackDispatcher) takeDue(ctx context.Context, limit int) ([]callbackAttempt, time.Duration) {
	next, err := d.store.nextCallbackAttempt(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("read next workflow callback: %v", err)
		}
		return nil, callbackRecheck
	}
	if !next.Valid {
		return nil, callbackRecheck
	}
	now := time.Now()
	if delay := time.UnixMilli(next.Int64).Sub(now); delay > 0 {
		return nil, min(delay, callbackRecheck)
	}
	attempts, err := d.store.claimCallbacks(ctx, now, limit)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("claim workflow callbacks: %v", err)
		}
		return nil, callbackRecheck
	}
	delay := callbackRecheck
	if len(attempts) > 0 {
		// The next due time may precede the periodic recovery check.
		delay = 0
	}
	return attempts, delay
}

func (d *callbackDispatcher) deliver(ctx context.Context, attempt callbackAttempt) {
	// Leave time to record the outcome before another process can claim this row.
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(attempt.leasedUntil).Add(-5*time.Second))
	defer cancel()
	message, retry := d.send(ctx, attempt)
	if message != "" && ctx.Err() != nil {
		// An interrupted attempt becomes due again when its lease expires.
		return
	}

	now := time.Now()
	var next, deliveredAt *int64
	if message == "" {
		stamp := now.UnixMilli()
		deliveredAt = &stamp
	} else if retry && attempt.attempts+1 < callbackMaxAttempts {
		backoff := (5 * time.Second) << attempt.attempts
		due := now.Add(backoff + rand.N(backoff/4)).UnixMilli()
		next = &due
	}
	// Record the outcome independently of shutdown once a receiver has replied.
	persistCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := d.store.completeCallbackAttempt(persistCtx, attempt, next, deliveredAt, message); err != nil {
		log.Printf("record workflow callback %d outcome: %v", attempt.taskID, err)
	} else if message != "" && next == nil {
		log.Printf("workflow callback %d failed after %d attempt(s): %s", attempt.taskID, attempt.attempts+1, message)
	}
}

func (d *callbackDispatcher) send(ctx context.Context, attempt callbackAttempt) (message string, retry bool) {
	task, err := d.store.GetTaskByID(ctx, attempt.taskID)
	if err != nil {
		return "load workflow callback task", !isNotFound(err)
	}
	var failedResult json.RawMessage
	if task.Status == StatusFailed {
		failedResult, err = d.store.failedStepResult(ctx, task.ID)
		if err != nil {
			return "load workflow callback error details", true
		}
	}
	body, err := buildCallbackPayload(*task, attempt.notifyURL, failedResult)
	if err != nil {
		return "encode workflow callback JSON", false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(attempt.notifyURL), bytes.NewReader(body))
	if err != nil || request.URL.Hostname() == "" || request.URL.User != nil ||
		(request.URL.Scheme != "https" && request.URL.Scheme != "http") {
		return "invalid workflow callback URL", false
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := d.client.Do(request)
	if err != nil {
		return "workflow callback request failed", true
	}
	defer response.Body.Close()
	// Small response bodies can be drained to reuse the persistent connection.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
	status := response.StatusCode
	if status >= 200 && status < 300 {
		return "", false
	}
	return fmt.Sprintf("workflow callback returned HTTP %d", status), status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests || status >= 500
}
