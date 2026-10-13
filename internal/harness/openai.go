package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []any         `json:"tools"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// providerError carries an HTTP status. Status 0 means the server was not reached.
type providerError struct {
	Status  int
	Message string
}

func (e *providerError) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("provider HTTP %d: %s", e.Status, e.Message)
}

func classifyProvider(err error) int {
	var pe *providerError
	if errors.As(err, &pe) {
		switch pe.Status {
		case 400, 401, 403, 404:
			return ExitConfigInvalid
		default:
			return ExitModelUnavailable
		}
	}
	return ExitModelUnavailable
}

var toolSchema = []any{
	fnTool("bash", "Run a shell command in the workspace.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
		"required": []string{"command"},
	}),
	fnTool("read", "Read a file in the workspace.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}),
	fnTool("write", "Write a file in the workspace.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []string{"path", "content"},
	}),
	fnTool("edit", "Replace one occurrence of old text in a workspace file.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"old":  map[string]any{"type": "string"},
			"new":  map[string]any{"type": "string"},
		},
		"required": []string{"path", "old", "new"},
	}),
	fnTool("grep", "Search workspace files with a regular expression.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string"},
		},
		"required": []string{"pattern"},
	}),
	fnTool("glob", "List workspace files matching a glob. ** matches across directories.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
		},
		"required": []string{"pattern"},
	}),
}

func fnTool(name, desc string, params map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  params,
		},
	}
}

func completeChat(ctx context.Context, baseURL, apiKey string, req chatRequest) (chatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return chatResponse{}, err
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, &providerError{Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(httpReq)
	if err != nil {
		return chatResponse{}, &providerError{Message: err.Error()}
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: err.Error()}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed chatResponse
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: msg}
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: "response was not JSON"}
	}
	if len(parsed.Choices) == 0 {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: "response had no choices"}
	}
	return parsed, nil
}
