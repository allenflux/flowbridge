package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func buildCallbackPayload(task WorkflowTask, notifyURL string, failedResult json.RawMessage) ([]byte, error) {
	req := normalizeStoredWorkflowRequest(task.WorkflowType, requestFromRaw(task.RequestPayload))
	payload := map[string]any{
		"task_type":    callbackTaskType(task.WorkflowType),
		"source_path":  req.SourcePath,
		"scene_name":   defaultString(req.VideoSceneName, req.SceneName),
		"fee":          defaultString(req.Fee, "10"),
		"app_id":       req.AppID,
		"title":        req.Title,
		"hash_key":     req.HashKey,
		"is_encrypt":   req.IsEncrypt,
		"is_watermark": watermarkEnabled(req.IsWatermark),
		"video_format": defaultString(req.VideoFormat, "video/h264-mp4"),
		"out_data":     json.RawMessage(`[]`),
	}
	switch task.Status {
	case StatusSuccess:
		var result map[string]json.RawMessage
		if len(task.FinalResult) > 0 {
			if err := json.Unmarshal(task.FinalResult, &result); err != nil {
				return nil, fmt.Errorf("decode final result for callback: %w", err)
			}
		}
		// Keep the final P0 document's field types and nested object order.
		for field, value := range result {
			switch strings.ToLower(field) {
			case "_id", "meta", "messages", "request_payload", "response_payload", "backend_headers", "headers",
				"api_key", "api-key", "apikey", "x-api-key", "authorization", "proxy-authorization", "cookie",
				"backend_task_id", "backend_uuid", "workflow_task_id", "notify_status", "notify_error", "notify_time", "sign":
				continue
			}
			payload[field] = value
		}
		payload["error_code"] = "SUCCESS"
	case StatusFailed:
		var result map[string]any
		_ = json.Unmarshal(failedResult, &result)
		message := task.ErrorMessage
		code := "FAILED"
		if status, ok := parseBackendStatus(result); ok && status == StatusFailed {
			if backendError, ok := result["error"].(string); ok && backendError != "" {
				message = backendError
			}
			if backendCode, ok := result["error_code"].(string); ok && backendCode != "" {
				code = backendCode
			}
		}
		if apiKey := strings.TrimSpace(taskAPIKey(task.RequestPayload, req.APIKey)); apiKey != "" {
			message = strings.ReplaceAll(message, apiKey, "****")
			message = strings.ReplaceAll(message, url.QueryEscape(apiKey), "****")
		}
		payload["error"] = defaultString(message, "workflow failed")
		payload["error_code"] = code
	default:
		return nil, fmt.Errorf("cannot build callback for task status %d", task.Status)
	}

	payload["task_id"] = task.TaskID
	payload["uuid"] = task.TaskID
	payload["bid"] = defaultString(req.BID, task.TaskID)
	payload["notify_url"] = notifyURL
	payload["status"] = task.Status
	payload["created_at"] = p0CallbackTime(task.CreatedAt)
	payload["updated_at"] = p0CallbackTime(task.UpdatedAt)
	if task.FinishedAt != nil {
		if _, ok := payload["finished_at"]; ok {
			payload["finished_at"] = p0CallbackTime(*task.FinishedAt)
		}
	}

	// encoding/json sorts map keys and preserves the order inside each RawMessage.
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sign, err := signP0Callback(body)
	if err != nil {
		return nil, err
	}
	body = append(body[:len(body)-1], `,"sign":"`...)
	body = append(body, sign...)
	return append(body, '"', '}'), nil
}

func callbackTaskType(workflowType string) string {
	switch workflowType {
	case WorkflowAnimeUndressVideo:
		return "generate_undress_anime_image_to_video"
	case WorkflowTenErosImageVideo, WorkflowTenErosBatch2ImageVideo:
		return "generate_videos_scenes_ltx_8s"
	case WorkflowMinimaxH3ImageVideo:
		return "videos_scenes_h3"
	default:
		return workflowType
	}
}

func p0CallbackTime(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

// signP0Callback consumes a JSON object whose top-level keys are already sorted.
func signP0Callback(body []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return "", err
	}
	var signingText strings.Builder
	var lastField string
	for dec.More() {
		field, err := dec.Token()
		if err != nil {
			return "", err
		}
		lastField = field.(string)
		value, err := dec.Token()
		if err != nil {
			return "", err
		}
		if value == "" {
			continue
		}
		signingText.WriteString(lastField)
		signingText.WriteByte('=')
		if err := appendP0CallbackValue(&signingText, dec, value, false); err != nil {
			return "", err
		}
		signingText.WriteByte('&')
	}
	if _, err := dec.Token(); err != nil {
		return "", err
	}
	// P0's sorting loop shadows its hash_key argument with the final field name.
	signingText.WriteString("key=")
	signingText.WriteString(lastField)
	checksum := md5.Sum([]byte(signingText.String()))
	return hex.EncodeToString(checksum[:]), nil
}

func appendP0CallbackValue(out *strings.Builder, dec *json.Decoder, value json.Token, nested bool) error {
	switch value := value.(type) {
	case nil:
		out.WriteString("None")
	case bool:
		if value {
			out.WriteString("True")
		} else {
			out.WriteString("False")
		}
	case string:
		if nested {
			appendP0CallbackString(out, value)
		} else {
			out.WriteString(value)
		}
	case json.Number:
		return appendP0CallbackNumber(out, value)
	case json.Delim:
		return appendP0CallbackCollection(out, dec, value)
	}
	return nil
}

func appendP0CallbackCollection(out *strings.Builder, dec *json.Decoder, start json.Delim) error {
	out.WriteByte(byte(start))
	separator := ""
	for dec.More() {
		out.WriteString(separator)
		separator = ", "
		if start == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			appendP0CallbackString(out, key.(string))
			out.WriteString(": ")
		}
		value, err := dec.Token()
		if err != nil {
			return err
		}
		if err := appendP0CallbackValue(out, dec, value, true); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	out.WriteByte(byte(end.(json.Delim)))
	return nil
}

func appendP0CallbackString(out *strings.Builder, value string) {
	quote := '\''
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	out.WriteRune(quote)
	for _, char := range value {
		switch {
		case char == quote || char == '\\':
			out.WriteByte('\\')
			out.WriteRune(char)
		case char == '\t':
			out.WriteString(`\t`)
		case char == '\n':
			out.WriteString(`\n`)
		case char == '\r':
			out.WriteString(`\r`)
		case unicode.IsPrint(char):
			out.WriteRune(char)
		case char <= 0xff:
			fmt.Fprintf(out, `\x%02x`, char)
		case char <= 0xffff:
			fmt.Fprintf(out, `\u%04x`, char)
		default:
			fmt.Fprintf(out, `\U%08x`, char)
		}
	}
	out.WriteRune(quote)
}

func appendP0CallbackNumber(out *strings.Builder, value json.Number) error {
	raw := value.String()
	if !strings.ContainsAny(raw, ".eE") {
		// Python decodes JSON -0 as an integer zero.
		if raw == "-0" {
			raw = "0"
		}
		out.WriteString(raw)
		return nil
	}
	number, err := value.Float64()
	if err != nil {
		return err
	}
	scientific := strconv.FormatFloat(number, 'e', -1, 64)
	_, exponent, _ := strings.Cut(scientific, "e")
	power, _ := strconv.Atoi(exponent)
	if power < -4 || power >= 16 {
		out.WriteString(scientific)
		return nil
	}
	decimal := strconv.FormatFloat(number, 'f', -1, 64)
	out.WriteString(decimal)
	if !strings.ContainsRune(decimal, '.') {
		out.WriteString(".0")
	}
	return nil
}
