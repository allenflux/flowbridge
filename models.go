package main

import (
	"encoding/json"
	"time"
)

const (
	StatusPending = 0
	StatusRunning = 1
	StatusSuccess = 2
	StatusFailed  = -1
)

const (
	WorkflowAnimeUndressVideo = "anime_undress_video"
	StepAnimeImage            = "anime_image"
	StepAnimeVideo            = "anime_video"
)

type WorkflowTask struct {
	ID             int64           `json:"id"`
	TaskID         string          `json:"task_id"`
	WorkflowType   string          `json:"workflow_type"`
	Status         int             `json:"status"`
	CurrentStep    string          `json:"current_step"`
	RequestPayload json.RawMessage `json:"request_payload,omitempty"`
	FinalResult    json.RawMessage `json:"final_result,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
}

type WorkflowStepRecord struct {
	ID              int64           `json:"id"`
	WorkflowTaskID  int64           `json:"workflow_task_id"`
	StepIndex       int             `json:"step_index"`
	StepName        string          `json:"step_name"`
	Status          int             `json:"status"`
	BackendTaskID   string          `json:"backend_task_id,omitempty"`
	RequestPayload  json.RawMessage `json:"request_payload,omitempty"`
	ResponsePayload json.RawMessage `json:"response_payload,omitempty"`
	ResultPayload   json.RawMessage `json:"result_payload,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type TaskDetail struct {
	WorkflowTask
	Steps []WorkflowStepRecord `json:"steps"`
}

type AnimeVideoRequest struct {
	SourcePath         string `json:"source_path"`
	SceneName          string `json:"scene_name"`
	VideoSceneName     string `json:"video_scene_name"`
	IncomingPrompt     string `json:"incoming_prompt"`
	QwenIncomingPrompt string `json:"qwen_incoming_prompt"`
	WanIncomingPrompt  string `json:"wan_incoming_prompt"`
	OutputFormat       string `json:"output_format"`
	BID                string `json:"bid"`
	AppID              string `json:"app_id"`
	Fee                string `json:"fee"`
	Title              string `json:"title"`
	HashKey            string `json:"hash_key"`
	APIKey             string `json:"apikey"`
	NotifyURL          string `json:"notify_url"`
	TaskID             string `json:"task_id"`
}

type PublicTaskResponse struct {
	UUID         string               `json:"uuid"`
	TaskID       string               `json:"task_id"`
	BID          string               `json:"bid,omitempty"`
	Fee          string               `json:"fee,omitempty"`
	AppID        string               `json:"app_id,omitempty"`
	Title        string               `json:"title,omitempty"`
	NotifyURL    string               `json:"notify_url,omitempty"`
	Status       int                  `json:"status"`
	TaskType     string               `json:"task_type"`
	SourcePath   string               `json:"source_path,omitempty"`
	SceneName    string               `json:"scene_name,omitempty"`
	OutputFormat string               `json:"output_format,omitempty"`
	CurrentStep  string               `json:"current_step,omitempty"`
	OutData      json.RawMessage      `json:"out_data,omitempty"`
	Error        string               `json:"error,omitempty"`
	Steps        []WorkflowStepRecord `json:"steps,omitempty"`
	CreatedAt    string               `json:"created_at,omitempty"`
	UpdatedAt    string               `json:"updated_at,omitempty"`
	FinishedAt   string               `json:"finished_at,omitempty"`
}
