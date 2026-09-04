package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Printf("flowbridge stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := LoadConfig()
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	backend := NewBackendClient(cfg)
	defer backend.CloseIdleConnections()
	worker := NewWorker(store, backend, cfg)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	worker.Start(workerCtx)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           NewServer(cfg, store, worker),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("flowbridge listening on %s, backend=%s, db=%s", cfg.Addr, cfg.BackendBaseURL, cfg.DBPath)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	var listenErr error
	select {
	case <-signalCtx.Done():
	case err := <-serverErr:
		listenErr = err
	}

	shutdownTimeout := cfg.RequestTimeout + 5*time.Second
	if shutdownTimeout < 10*time.Second {
		shutdownTimeout = 10 * time.Second
	}
	if shutdownTimeout > 30*time.Second {
		shutdownTimeout = 30 * time.Second
	}
	// Stop background work immediately while in-flight HTTP handlers drain. Both
	// phases share one budget so container shutdown cannot stretch indefinitely.
	cancelWorker()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
		if closeErr := server.Close(); closeErr != nil {
			log.Printf("force close server: %v", closeErr)
		}
	}
	if err := worker.Wait(shutdownCtx); err != nil {
		log.Printf("wait for workers: %v", err)
	}
	cancel()
	return listenErr
}
