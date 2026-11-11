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

	"github.com/polds/factory/internal/inference"
)

// Empty tool text is omitted by encoding/json, and OpenAI requires content.
// LiteLLM's Anthropic adapter leaves role "tool" unchanged
// (_sanitize_empty_text_content in prompt_templates/factory.py).
const (
	noToolOutput = "(no output)"
	noUserText   = "(no text)"
)

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// MarshalJSON is the only encoder for chatMessage. An assistant turn that
// only has tool_calls omits content. "" becomes an empty Anthropic text
// block, which Anthropic rejects. Omitting it is what turns 1-3 already
// sent. Null would also be valid.
func (m chatMessage) MarshalJSON() ([]byte, error) {
	wire := struct {
		Role       string     `json:"role"`
		Content    *string    `json:"content,omitempty"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if text, ok := outboundContent(m); ok {
		wire.Content = &text
	}
	return json.Marshal(wire)
}

func outboundContent(m chatMessage) (string, bool) {
	switch m.Role {
	case "tool":
		if strings.TrimSpace(m.Content) == "" {
			return noToolOutput, true
		}
		return m.Content, true
	case "user":
		if strings.TrimSpace(m.Content) == "" {
			return noUserText, true
		}
		return m.Content, true
	case "assistant":
		if len(m.ToolCalls) > 0 && strings.TrimSpace(m.Content) == "" {
			return "", false
		}
		if m.Content == "" {
			return "", false
		}
		return m.Content, true
	default:
		if m.Content == "" {
			return "", false
		}
		return m.Content, true
	}
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
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Tools     []any         `json:"tools"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model,omitempty"`
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
	// HeaderCost is x-litellm-response-cost in USD. Empty means the header was absent.
	HeaderCost string `json:"-"`
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

// statusRequestRejected stays on exit 20. Exit 21 would retry the same body.
const statusRequestRejected = "request_rejected"

// classifyProvider maps a provider failure to a contract exit.
// 401, 403, 404, and a 400 before any accepted call are configuration.
// A 400 after an accepted call on the same model and key is the request
// body, so the status names that instead of config_invalid.
func classifyProvider(err error, accepted, turn int) (code int, status, summary string) {
	summary = err.Error()
	var pe *providerError
	if !errors.As(err, &pe) {
		return ExitModelUnavailable, "model_unavailable", summary
	}
	switch pe.Status {
	case 402:
		return ExitConfigInvalid, "backend_allowance_exhausted", summary
	case 401, 403, 404:
		return ExitConfigInvalid, "config_invalid", summary
	case 400:
		if accepted > 0 {
			detail := pe.Message
			if detail == "" {
				detail = pe.Error()
			}
			summary = fmt.Sprintf("provider rejected request shape (HTTP 400) at turn %d: %s", turn, detail)
			return ExitConfigInvalid, statusRequestRejected, summary
		}
		return ExitConfigInvalid, "config_invalid", summary
	default:
		return ExitModelUnavailable, "model_unavailable", summary
	}
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
	fnTool("ask_input", "Ask a person a question and pause until they answer. Use this when the task needs a decision you cannot look up. Pass the question as text.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "The question to ask."},
		},
		"required": []string{"text"},
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

const defaultChatTimeout = 60 * time.Second

const litellmCostHeader = "x-litellm-response-cost"

func completeChat(ctx context.Context, baseURL, apiKey string, extra []inference.Header, req chatRequest, timeout time.Duration) (chatResponse, error) {
	if timeout <= 0 {
		timeout = defaultChatTimeout
	}
	body, err := json.Marshal(req)
	if err != nil {
		return chatResponse{}, err
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, &providerError{Message: redactProvider(err.Error(), apiKey, extra)}
	}
	inference.Apply(httpReq.Header, apiKey, extra)
	client := &http.Client{Timeout: timeout}
	res, err := client.Do(httpReq)
	if err != nil {
		return chatResponse{}, &providerError{Message: redactProvider(err.Error(), apiKey, extra)}
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: redactProvider(err.Error(), apiKey, extra)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed chatResponse
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: redactProvider(msg, apiKey, extra)}
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: "response was not JSON"}
	}
	if len(parsed.Choices) == 0 {
		return chatResponse{}, &providerError{Status: res.StatusCode, Message: "response had no choices"}
	}
	if cost := strings.TrimSpace(res.Header.Get(litellmCostHeader)); cost != "" {
		parsed.HeaderCost = cost
	}
	return parsed, nil
}

func redactProvider(msg, apiKey string, extra []inference.Header) string {
	msg = inference.Redact(msg, extra)
	if apiKey != "" {
		msg = Redact(msg, []string{apiKey})
	}
	return msg
}
