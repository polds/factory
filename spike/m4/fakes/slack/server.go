package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const maxBody = 1 << 20

// server is the offline Slack Web API. It never writes the bot token,
// request headers, or request bodies to the log.
type server struct {
	log   *slog.Logger
	token string

	mu    sync.Mutex
	next  int
	posts []slackMessage
}

type slackMessage struct {
	Op       string          `json:"op"`
	TS       string          `json:"ts"`
	ThreadTS string          `json:"thread_ts"`
	Channel  string          `json:"channel"`
	Text     string          `json:"text"`
	Blocks   json.RawMessage `json:"blocks,omitempty"`
}

type slackState struct {
	Messages []slackMessage `json:"messages"`
}

func newSlack(token string, log *slog.Logger) *server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &server{log: log, token: token}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeSlack(w, false, "", "bad_request")
		return
	}
	if len(body) > maxBody {
		writeSlack(w, false, "", "payload_too_large")
		return
	}
	s.log.Info("slack", "method", r.Method, "path", r.URL.Path)
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/_spike/state" {
		writeJSON(w, s.snapshot())
		return
	}
	if r.Method != http.MethodPost {
		writeSlack(w, false, "", "unknown_method")
		return
	}
	if !s.allow(r) {
		writeSlack(w, false, "", "invalid_auth")
		return
	}
	switch r.URL.Path {
	case "/chat.postMessage":
		s.post(w, body)
	case "/chat.update":
		s.update(w, body)
	default:
		writeSlack(w, false, "", "unknown_method")
	}
}

func (s *server) post(w http.ResponseWriter, body []byte) {
	var req struct {
		Channel  string          `json:"channel"`
		ThreadTS string          `json:"thread_ts"`
		Text     string          `json:"text"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Channel == "" {
		writeSlack(w, false, "", "invalid_arguments")
		return
	}
	s.mu.Lock()
	s.next++
	ts := "1700000000." + pad6(s.next)
	s.posts = append(s.posts, slackMessage{
		Op: "post", TS: ts, ThreadTS: req.ThreadTS, Channel: req.Channel, Text: req.Text,
		Blocks: keptBlocks(req.Blocks),
	})
	s.mu.Unlock()
	writeSlack(w, true, ts, "")
}

func (s *server) update(w http.ResponseWriter, body []byte) {
	var req struct {
		Channel string          `json:"channel"`
		TS      string          `json:"ts"`
		Text    string          `json:"text"`
		Blocks  json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.TS == "" {
		writeSlack(w, false, "", "invalid_arguments")
		return
	}
	s.mu.Lock()
	found := false
	thread := ""
	channel := req.Channel
	for _, row := range s.posts {
		if row.Op == "post" && row.TS == req.TS {
			found = true
			thread = row.ThreadTS
			if channel == "" {
				channel = row.Channel
			}
		}
	}
	if found {
		s.posts = append(s.posts, slackMessage{
			Op: "update", TS: req.TS, ThreadTS: thread, Channel: channel, Text: req.Text,
			Blocks: keptBlocks(req.Blocks),
		})
	}
	s.mu.Unlock()
	if !found {
		writeSlack(w, false, "", "message_not_found")
		return
	}
	writeSlack(w, true, req.TS, "")
}

func (s *server) snapshot() slackState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slackState{Messages: append([]slackMessage(nil), s.posts...)}
}

func (s *server) allow(r *http.Request) bool {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	got := strings.TrimSpace(value[len(prefix):])
	if got == "" {
		return false
	}
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func keptBlocks(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("[]")) {
		return nil
	}
	return raw
}

func pad6(n int) string {
	text := strconv.Itoa(n)
	if len(text) >= 6 {
		return text
	}
	return strings.Repeat("0", 6-len(text)) + text
}

func writeSlack(w http.ResponseWriter, ok bool, ts, code string) {
	body := map[string]any{"ok": ok}
	if ts != "" {
		body["ts"] = ts
	}
	if code != "" {
		body["error"] = code
	}
	writeJSON(w, body)
}

func writeJSON(w http.ResponseWriter, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
