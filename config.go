package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr              string        `json:"addr"`
	BackendBaseURL    string        `json:"backend_base_url"`
	DBPath            string        `json:"db_path"`
	WorkerConcurrency int           `json:"worker_concurrency"`
	WorkerQueueSize   int           `json:"worker_queue_size"`
	MaxPollErrors     int           `json:"max_poll_errors"`
	PollInterval      time.Duration `json:"-"`
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
		MaxPollErrors:     10,
		PollInterval:      3 * time.Second,
		TaskTimeout:       30 * time.Minute,
		HTTPTimeout:       30 * time.Second,
	}
}

type fileConfig struct {
	Addr              *string `json:"addr"`
	BackendBaseURL    *string `json:"backend_base_url"`
	DBPath            *string `json:"db_path"`
	WorkerConcurrency *int    `json:"worker_concurrency"`
	WorkerQueueSize   *int    `json:"worker_queue_size"`
	MaxPollErrors     *int    `json:"max_poll_errors"`
	PollInterval      *string `json:"poll_interval"`
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
	if file.MaxPollErrors != nil {
		cfg.MaxPollErrors = *file.MaxPollErrors
	}
	if file.PollInterval != nil {
		cfg.PollInterval = parseDuration(*file.PollInterval, cfg.PollInterval)
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
	cfg.MaxPollErrors = envInt("FLOWBRIDGE_MAX_POLL_ERRORS", cfg.MaxPollErrors)
	cfg.PollInterval = envDuration("FLOWBRIDGE_POLL_INTERVAL", cfg.PollInterval)
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
	if cfg.MaxPollErrors <= 0 {
		cfg.MaxPollErrors = 10
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 30 * time.Minute
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
