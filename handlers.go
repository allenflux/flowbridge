package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	cfg    Config
	store  *Store
	worker *Worker
	mux    *http.ServeMux
	admin  *template.Template
}

func NewServer(cfg Config, store *Store, worker *Worker) *Server {
	s := &Server{
		cfg:    cfg,
		store:  store,
		worker: worker,
		mux:    http.NewServeMux(),
		admin: template.Must(template.New("admin.html").Funcs(template.FuncMap{
			"statusText":  statusText,
			"statusClass": statusClass,
			"prettyJSON":  prettyJSON,
			"formatTime":  formatTime,
			"jsonField":   jsonField,
			"requestKey":  requestKey,
		}).ParseFS(templateFS, "templates/admin.html")),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
		}
	}()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("POST /api/public/generate/undress/anime/video", s.createAnimeVideoWorkflow)
	s.mux.HandleFunc("POST /api/public/workflow/undress/anime/video", s.createAnimeVideoWorkflow)
	s.mux.HandleFunc("GET /api/public/task", s.getPublicTask)
	s.mux.HandleFunc("POST /api/public/task", s.getPublicTask)
	s.mux.HandleFunc("GET /admin/workflows", s.adminList)
	s.mux.HandleFunc("GET /admin/workflows/", s.adminDetail)
	s.mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/workflows", http.StatusFound)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) createAnimeVideoWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	req, err := parseAnimeVideoRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.SourcePath) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "source_path is required"})
		return
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		taskID = "bridge_" + randomHex(16)
	}
	req.TaskID = taskID
	if req.Fee == "" {
		req.Fee = "10"
	}
	if req.OutputFormat == "" {
		req.OutputFormat = "video"
	}
	if req.QwenIncomingPrompt == "" {
		req.QwenIncomingPrompt = req.IncomingPrompt
	}
	raw, _ := json.Marshal(req)
	task, err := s.store.CreateAnimeVideoTask(r.Context(), taskID, req, raw)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	s.worker.Enqueue(task.ID)
	resp := publicResponse(TaskDetail{WorkflowTask: *task}, req, false)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getPublicTask(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	taskID, err := taskIDFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	detail, err := s.store.GetTaskDetail(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, publicTaskQueryResponse(*detail))
}

func (s *Server) adminList(w http.ResponseWriter, r *http.Request) {
	queryTaskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if queryTaskID != "" {
		if _, err := s.store.GetTaskDetail(r.Context(), queryTaskID); err == nil {
			http.Redirect(w, r, "/admin/workflows/"+queryTaskID, http.StatusFound)
			return
		}
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	total, err := s.store.CountTasks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize
	tasks, err := s.store.ListTasks(r.Context(), pageSize, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.admin.ExecuteTemplate(w, "list", map[string]any{
		"Title":      "FlowBridge Workflows",
		"Tasks":      tasks,
		"TaskID":     queryTaskID,
		"Now":        time.Now(),
		"Backend":    s.cfg.BackendBaseURL,
		"Page":       page,
		"PageSize":   pageSize,
		"Total":      total,
		"TotalPages": totalPages,
		"PrevPage":   page - 1,
		"NextPage":   page + 1,
		"HasPrev":    page > 1,
		"HasNext":    page < totalPages,
	}); err != nil {
		log.Printf("render admin list failed: %v", err)
	}
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *Server) adminDetail(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/admin/workflows/")
	taskID = strings.Trim(taskID, "/")
	if taskID == "" {
		http.Redirect(w, r, "/admin/workflows", http.StatusFound)
		return
	}
	detail, err := s.store.GetTaskDetail(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.admin.ExecuteTemplate(w, "detail", map[string]any{
		"Title":  "Workflow Detail",
		"Task":   detail.WorkflowTask,
		"Steps":  detail.Steps,
		"Req":    requestFromRaw(detail.RequestPayload),
		"Detail": detail,
	}); err != nil {
		log.Printf("render admin detail failed: %v", err)
	}
}

func parseAnimeVideoRequest(r *http.Request) (AnimeVideoRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var req AnimeVideoRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			return req, err
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return req, err
		}
		req.APIKey = firstNonEmpty(req.APIKey, r.Header.Get("Apikey"), r.Header.Get("X-API-Key"))
		return req, nil
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			return AnimeVideoRequest{}, err
		}
	}
	form := r.Form
	return AnimeVideoRequest{
		SourcePath:         form.Get("source_path"),
		SceneName:          form.Get("scene_name"),
		VideoSceneName:     form.Get("video_scene_name"),
		IncomingPrompt:     form.Get("incoming_prompt"),
		QwenIncomingPrompt: form.Get("qwen_incoming_prompt"),
		WanIncomingPrompt:  form.Get("wan_incoming_prompt"),
		OutputFormat:       form.Get("output_format"),
		BID:                form.Get("bid"),
		AppID:              form.Get("app_id"),
		Fee:                form.Get("fee"),
		Title:              form.Get("title"),
		HashKey:            form.Get("hash_key"),
		APIKey:             firstNonEmpty(form.Get("apikey"), form.Get("Apikey"), form.Get("api_key"), r.Header.Get("Apikey"), r.Header.Get("X-API-Key")),
		NotifyURL:          form.Get("notify_url"),
		TaskID:             form.Get("task_id"),
	}, nil
}

func taskIDFromRequest(r *http.Request) (string, error) {
	if value := strings.TrimSpace(r.URL.Query().Get("task_id")); value != "" {
		return value, nil
	}
	if r.Method == http.MethodPost {
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			var body map[string]any
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return "", err
			}
			if value, ok := body["task_id"].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		} else {
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				_ = r.ParseForm()
			}
			if value := strings.TrimSpace(r.Form.Get("task_id")); value != "" {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("task_id is required")
}

func publicResponse(detail TaskDetail, req AnimeVideoRequest, includeSteps bool) PublicTaskResponse {
	resp := PublicTaskResponse{
		UUID:         detail.TaskID,
		TaskID:       detail.TaskID,
		BID:          req.BID,
		Fee:          req.Fee,
		AppID:        req.AppID,
		Title:        req.Title,
		NotifyURL:    req.NotifyURL,
		Status:       detail.Status,
		TaskType:     WorkflowAnimeUndressVideo,
		SourcePath:   req.SourcePath,
		SceneName:    defaultString(req.VideoSceneName, req.SceneName),
		OutputFormat: req.OutputFormat,
		CurrentStep:  detail.CurrentStep,
		OutData:      detail.FinalResult,
		Error:        detail.ErrorMessage,
		CreatedAt:    detail.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    detail.UpdatedAt.Format(time.RFC3339),
	}
	if detail.FinishedAt != nil {
		resp.FinishedAt = detail.FinishedAt.Format(time.RFC3339)
	}
	if includeSteps {
		resp.Steps = detail.Steps
	}
	return resp
}

func publicTaskQueryResponse(detail TaskDetail) any {
	if detail.Status == StatusSuccess && len(detail.FinalResult) > 0 {
		var final any
		if err := json.Unmarshal(detail.FinalResult, &final); err == nil {
			return final
		}
		return detail.FinalResult
	}
	resp := map[string]any{
		"task_id":      detail.TaskID,
		"status":       detail.Status,
		"task_type":    detail.WorkflowType,
		"current_step": detail.CurrentStep,
		"created_at":   detail.CreatedAt.Format(time.RFC3339),
		"updated_at":   detail.UpdatedAt.Format(time.RFC3339),
	}
	if detail.ErrorMessage != "" {
		resp["error"] = detail.ErrorMessage
	}
	if detail.FinishedAt != nil {
		resp["finished_at"] = detail.FinishedAt.Format(time.RFC3339)
	}
	return resp
}

func requestFromRaw(raw json.RawMessage) AnimeVideoRequest {
	var req AnimeVideoRequest
	_ = json.Unmarshal(raw, &req)
	return req
}

func redactedRequest(req AnimeVideoRequest) AnimeVideoRequest {
	req.APIKey = redactSecret(req.APIKey)
	return req
}

func redactSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomHex(bytesLen int) string {
	data := make([]byte, bytesLen)
	if _, err := rand.Read(data); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(data)
}

func statusText(status int) string {
	switch status {
	case StatusPending:
		return "Pending"
	case StatusRunning:
		return "Running"
	case StatusSuccess:
		return "Success"
	case StatusFailed:
		return "Failed"
	case 3:
		return "Watermarking"
	default:
		return "Unknown"
	}
}

func statusClass(status int) string {
	switch status {
	case StatusPending:
		return "secondary"
	case StatusRunning:
		return "primary"
	case StatusSuccess:
		return "success"
	case StatusFailed:
		return "danger"
	case 3:
		return "info"
	default:
		return "dark"
	}
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	formatted, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(formatted)
}

func jsonField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	value, ok := decoded[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		rawValue, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(rawValue)
	}
}

func requestKey(raw json.RawMessage, key string) string {
	value := jsonField(raw, key)
	if value == "" {
		return ""
	}
	if key == "apikey" || key == "api_key" || strings.EqualFold(key, "apikey") {
		return redactSecret(value)
	}
	return value
}

func formatTime(t any) string {
	switch typed := t.(type) {
	case time.Time:
		if typed.IsZero() {
			return "-"
		}
		return typed.Local().Format("2006-01-02 15:04:05")
	case *time.Time:
		if typed == nil || typed.IsZero() {
			return "-"
		}
		return typed.Local().Format("2006-01-02 15:04:05")
	default:
		return "-"
	}
}
