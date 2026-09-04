package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultTaskTimeout    = 30 * time.Minute
)

type Config struct {
	Addr              string        `json:"addr"`
	BackendBaseURL    string        `json:"backend_base_url"`
	DBPath            string        `json:"db_path"`
	WorkerConcurrency int           `json:"worker_concurrency"`
	WorkerQueueSize   int           `json:"worker_queue_size"`
	MaxRunnableTasks  int           `json:"max_runnable_tasks"`
	MaxSubmitRetries  int           `json:"max_submit_retries"`
	MaxPollErrors     int           `json:"max_poll_errors"`
	MaxTaskNotFound   int           `json:"max_task_not_found"`
	PollInterval      time.Duration `json:"-"`
	RequestTimeout    time.Duration `json:"-"`
	TaskTimeout       time.Duration `json:"-"`
	HTTPTimeout       time.Duration `json:"-"`
}

func LoadConfig() Config {
	cfg := defaultConfig()
	configPath := envString("FLOWBRIDGE_CONFIG", "config.json")
	if err := loadConfigFile(configPath, &cfg); err != nil {
		log.Printf("load config file %s failed: %v", configPath, err)
	}
	applyEnvOverrides(&cfg)
	normalizeConfig(&cfg)
	return cfg
}

func defaultConfig() Config {
	return Config{
		Addr:              ":8080",
		BackendBaseURL:    "http://localhost:8000",
		DBPath:            "flowbridge.db",
		WorkerConcurrency: 4,
		WorkerQueueSize:   10000,
		MaxRunnableTasks:  10000,
		MaxSubmitRetries:  8,
		MaxPollErrors:     10,
		MaxTaskNotFound:   60,
		PollInterval:      3 * time.Second,
		RequestTimeout:    defaultRequestTimeout,
		TaskTimeout:       defaultTaskTimeout,
		HTTPTimeout:       30 * time.Second,
	}
}

type fileConfig struct {
	Addr              *string `json:"addr"`
	BackendBaseURL    *string `json:"backend_base_url"`
	DBPath            *string `json:"db_path"`
	WorkerConcurrency *int    `json:"worker_concurrency"`
	WorkerQueueSize   *int    `json:"worker_queue_size"`
	MaxRunnableTasks  *int    `json:"max_runnable_tasks"`
	MaxSubmitRetries  *int    `json:"max_submit_retries"`
	MaxPollErrors     *int    `json:"max_poll_errors"`
	MaxTaskNotFound   *int    `json:"max_task_not_found"`
	PollInterval      *string `json:"poll_interval"`
	RequestTimeout    *string `json:"request_timeout"`
	TaskTimeout       *string `json:"task_timeout"`
	HTTPTimeout       *string `json:"http_timeout"`
}

func loadConfigFile(path string, cfg *Config) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file fileConfig
	if err := json.Unmarshal(raw, &file); err != nil {
		return err
	}
	if file.Addr != nil {
		cfg.Addr = *file.Addr
	}
	if file.BackendBaseURL != nil {
		cfg.BackendBaseURL = *file.BackendBaseURL
	}
	if file.DBPath != nil {
		cfg.DBPath = *file.DBPath
	}
	if file.WorkerConcurrency != nil {
		cfg.WorkerConcurrency = *file.WorkerConcurrency
	}
	if file.WorkerQueueSize != nil {
		cfg.WorkerQueueSize = *file.WorkerQueueSize
	}
	if file.MaxRunnableTasks != nil {
		cfg.MaxRunnableTasks = *file.MaxRunnableTasks
	}
	if file.MaxSubmitRetries != nil {
		cfg.MaxSubmitRetries = *file.MaxSubmitRetries
	}
	if file.MaxPollErrors != nil {
		cfg.MaxPollErrors = *file.MaxPollErrors
	}
	if file.MaxTaskNotFound != nil {
		cfg.MaxTaskNotFound = *file.MaxTaskNotFound
	}
	if file.PollInterval != nil {
		cfg.PollInterval = parseDuration(*file.PollInterval, cfg.PollInterval)
	}
	if file.RequestTimeout != nil {
		cfg.RequestTimeout = parseDuration(*file.RequestTimeout, cfg.RequestTimeout)
	}
	if file.TaskTimeout != nil {
		cfg.TaskTimeout = parseDuration(*file.TaskTimeout, cfg.TaskTimeout)
	}
	if file.HTTPTimeout != nil {
		cfg.HTTPTimeout = parseDuration(*file.HTTPTimeout, cfg.HTTPTimeout)
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	cfg.Addr = envString("FLOWBRIDGE_ADDR", cfg.Addr)
	cfg.BackendBaseURL = envString("BACKEND_BASE_URL", cfg.BackendBaseURL)
	cfg.DBPath = envString("FLOWBRIDGE_DB_PATH", cfg.DBPath)
	cfg.WorkerConcurrency = envInt("FLOWBRIDGE_WORKERS", cfg.WorkerConcurrency)
	cfg.WorkerQueueSize = envInt("FLOWBRIDGE_QUEUE_SIZE", cfg.WorkerQueueSize)
	cfg.MaxRunnableTasks = envInt("FLOWBRIDGE_MAX_RUNNABLE_TASKS", cfg.MaxRunnableTasks)
	cfg.MaxSubmitRetries = envNonNegativeInt("FLOWBRIDGE_MAX_SUBMIT_RETRIES", cfg.MaxSubmitRetries)
	cfg.MaxPollErrors = envInt("FLOWBRIDGE_MAX_POLL_ERRORS", cfg.MaxPollErrors)
	cfg.MaxTaskNotFound = envInt("FLOWBRIDGE_MAX_TASK_NOT_FOUND", cfg.MaxTaskNotFound)
	cfg.PollInterval = envDuration("FLOWBRIDGE_POLL_INTERVAL", cfg.PollInterval)
	cfg.RequestTimeout = envDuration("FLOWBRIDGE_REQUEST_TIMEOUT", cfg.RequestTimeout)
	cfg.TaskTimeout = envDuration("FLOWBRIDGE_TASK_TIMEOUT", cfg.TaskTimeout)
	cfg.HTTPTimeout = envDuration("FLOWBRIDGE_HTTP_TIMEOUT", cfg.HTTPTimeout)
}

func normalizeConfig(cfg *Config) {
	cfg.BackendBaseURL = strings.TrimRight(cfg.BackendBaseURL, "/")
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.BackendBaseURL == "" {
		cfg.BackendBaseURL = "http://localhost:8000"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "flowbridge.db"
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 4
	}
	if cfg.WorkerQueueSize < cfg.WorkerConcurrency {
		cfg.WorkerQueueSize = cfg.WorkerConcurrency
	}
	if cfg.MaxRunnableTasks <= 0 {
		cfg.MaxRunnableTasks = cfg.WorkerQueueSize
	}
	if cfg.MaxSubmitRetries < 0 {
		cfg.MaxSubmitRetries = 8
	}
	if cfg.MaxPollErrors <= 0 {
		cfg.MaxPollErrors = 10
	}
	if cfg.MaxTaskNotFound <= 0 {
		cfg.MaxTaskNotFound = 60
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = defaultTaskTimeout
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envNonNegativeInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return parseDuration(value, fallback)
}

func parseDuration(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err == nil {
		return parsed
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
