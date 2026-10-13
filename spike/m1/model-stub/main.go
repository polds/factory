// Command model-stub is a deterministic OpenAI-compatible model for offline
// M1 runs. Point OPENAI_BASE_URL at it and pass a JSON script with -script.
//
// Each script element is either {"tool":"<name>","args":{...}} or
// {"final":"<text>"}. A tool step becomes one assistant function call
// (id call_<n>, arguments the args object encoded as a JSON string). A final
// step becomes assistant content and no tool calls. POST /chat/completions
// and POST /v1/chat/completions each return the next step. After the script
// ends, later requests repeat the last final step. GET /healthz returns 200.
// The process logs the chat request count and never the request body.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const maxRequestBytes = 1 << 20

type stepKind string

const (
	kindTool  stepKind = "tool"
	kindFinal stepKind = "final"
)

// step is one scripted model turn. args is a compact JSON object.
type step struct {
	kind stepKind
	tool string
	args string
	text string
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	script := flag.String("script", "", "path to a JSON script")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: model-stub [-addr host:port] -script PATH\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatalf("model-stub: unexpected arguments")
	}
	if *script == "" {
		log.Fatalf("model-stub: -script is required")
	}
	steps, err := loadScript(*script)
	if err != nil {
		log.Fatalf("model-stub: %v", err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           newHandler(steps),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("listening addr=%s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("model-stub: %v", err)
	}
}

func loadScript(path string) ([]step, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read script: %w", err)
	}
	return parseScript(b)
}

func parseScript(b []byte) ([]step, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	steps := make([]step, 0, len(raw))
	for i, item := range raw {
		st, err := parseStep(item)
		if err != nil {
			return nil, fmt.Errorf("script step %d: %w", i, err)
		}
		steps = append(steps, st)
	}
	return steps, nil
}

func parseStep(item json.RawMessage) (step, error) {
	if len(bytes.TrimSpace(item)) == 0 || bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
		return step{}, errors.New("step must be an object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return step{}, fmt.Errorf("decode: %w", err)
	}
	for key := range fields {
		switch key {
		case "tool", "args", "final":
		default:
			return step{}, fmt.Errorf("unknown field %q", key)
		}
	}
	_, hasTool := fields["tool"]
	_, hasFinal := fields["final"]
	_, hasArgs := fields["args"]
	switch {
	case hasTool && hasFinal:
		return step{}, errors.New("step has both tool and final")
	case hasFinal:
		if hasArgs {
			return step{}, errors.New("final step cannot include args")
		}
		text, err := decodeString(fields["final"])
		if err != nil {
			return step{}, fmt.Errorf("final: %w", err)
		}
		return step{kind: kindFinal, text: text}, nil
	case hasTool:
		name, err := decodeString(fields["tool"])
		if err != nil {
			return step{}, fmt.Errorf("tool: %w", err)
		}
		if name == "" {
			return step{}, errors.New("tool name is empty")
		}
		args := "{}"
		if hasArgs {
			args, err = compactObject(fields["args"])
			if err != nil {
				return step{}, fmt.Errorf("args: %w", err)
			}
		}
		return step{kind: kindTool, tool: name, args: args}, nil
	default:
		return step{}, errors.New("step needs tool or final")
	}
}

func decodeString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func compactObject(raw json.RawMessage) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	if buf.Len() == 0 || buf.Bytes()[0] != '{' {
		return "", errors.New("must be a JSON object")
	}
	return buf.String(), nil
}

type scriptHandler struct {
	mu    sync.Mutex
	steps []step
	n     int
}

func newHandler(steps []step) http.Handler {
	cp := append([]step(nil), steps...)
	return &scriptHandler{steps: cp}
}

func (h *scriptHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodPost && isChatPath(r.URL.Path) {
		h.serveChat(w, r)
		return
	}
	http.NotFound(w, r)
}

func isChatPath(path string) bool {
	return path == "/chat/completions" || path == "/v1/chat/completions"
}

func (h *scriptHandler) serveChat(w http.ResponseWriter, r *http.Request) {
	// Discard the body so it cannot be logged and the connection can finish.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	st, n, ok := h.advance()
	log.Printf("request count=%d", n)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": "script has no final step"},
		})
		return
	}
	msg, err := assistantMessage(st, n)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": "internal error"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"choices": []any{map[string]any{"message": msg}},
	})
}

func (h *scriptHandler) advance() (step, int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	if h.n <= len(h.steps) {
		return h.steps[h.n-1], h.n, true
	}
	st, ok := lastFinal(h.steps)
	return st, h.n, ok
}

func lastFinal(steps []step) (step, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].kind == kindFinal {
			return steps[i], true
		}
	}
	return step{}, false
}

func assistantMessage(st step, n int) (map[string]any, error) {
	switch st.kind {
	case kindTool:
		return map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   fmt.Sprintf("call_%d", n),
				"type": "function",
				"function": map[string]any{
					"name":      st.tool,
					"arguments": st.args,
				},
			}},
		}, nil
	case kindFinal:
		return map[string]any{
			"role":    "assistant",
			"content": st.text,
		}, nil
	default:
		unknown := st.kind
		return nil, fmt.Errorf("unhandled step kind %q", string(unknown))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
