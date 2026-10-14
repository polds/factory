package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWatchReconnectsWithLastEventID(t *testing.T) {
	var mu sync.Mutex
	var resumes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/runs/run_abc/events" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		last := r.Header.Get("Last-Event-ID")
		mu.Lock()
		resumes = append(resumes, last)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		if last == "" {
			writeSSE(t, w, "1", "message.delta", `{"role":"assistant","text":"one"}`)
			writeSSE(t, w, "2", "tool.call", `{"tool":"bash","redacted_input":"echo one"}`)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if last != "2" {
			http.Error(w, "bad resume "+last, http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("from_seq") != "" && r.URL.Query().Get("from_seq") != "2" {
			http.Error(w, "bad from_seq", http.StatusBadRequest)
			return
		}
		writeSSE(t, w, "3", "message.delta", `{"role":"assistant","text":"three"}`)
		writeSSE(t, w, "4", "terminal", `{"status":"succeeded","summary":"done"}`)
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	c := &Client{BaseURL: srv.URL, APIKey: "test-key", Backoff: func(int) time.Duration { return 0 }}
	code, err := c.Watch(ctx, "run_abc", nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	text := out.String()
	for _, line := range []string{
		"message.delta role=assistant text=one",
		"tool.call tool=bash redacted_input=echo one",
		"message.delta role=assistant text=three",
		"terminal status=succeeded summary=done",
	} {
		if strings.Count(text, line) != 1 {
			t.Fatalf("line %q count %d in\n%s", line, strings.Count(text, line), text)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumes) < 2 || resumes[0] != "" || resumes[1] != "2" {
		t.Fatalf("resumes %q", resumes)
	}
}

func TestWatchExitCodes(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"succeeded", 0},
		{"failed", 1},
		{"cancelled", 2},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeSSE(t, w, "1", "terminal", fmt.Sprintf(`{"status":%q}`, tc.status))
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c := &Client{BaseURL: srv.URL, APIKey: "test-key", Backoff: func(int) time.Duration { return 0 }}
			code, err := c.Watch(ctx, "run_x", nil, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if code != tc.want {
				t.Fatalf("exit %d want %d", code, tc.want)
			}
		})
	}
}

func TestWatchFromSeq(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("from_seq")
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, "8", "run.terminal", `{"status":"succeeded"}`)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	from := int64(7)
	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	var out strings.Builder
	code, err := c.Watch(ctx, "run_x", &from, &out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got != "7" {
		t.Fatalf("from_seq %q", got)
	}
	if !strings.Contains(out.String(), "terminal status=succeeded") {
		t.Fatalf("output %q", out.String())
	}
}

func writeSSE(t *testing.T, w http.ResponseWriter, id, event, data string) {
	t.Helper()
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, data); err != nil {
		t.Errorf("write sse: %v", err)
	}
}
