package outbound

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/chat/slack"
	"github.com/polds/factory/internal/forge/github"
)

func TestRealClientsHideTokens(t *testing.T) {
	const (
		ghCanary    = "ghs_CANARY_TOKEN_DO_NOT_LOG"
		slackCanary = "xoxb-CANARY-DO-NOT-LOG"
	)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var ghPaths []string
	var slackPosts int
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		ghPaths = append(ghPaths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":      ghCanary,
				"expires_at": now.Add(time.Hour).Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":11}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/check-runs"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ghSrv.Close)
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		slackPosts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1500000000.000100"}`))
	}))
	t.Cleanup(slackSrv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ghClient, err := github.New(github.Config{
		AppID:   4242,
		Key:     key,
		BaseURL: ghSrv.URL,
		Now:     func() time.Time { return now },
		Log:     logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	slackClient := slack.New(slackCanary, slack.WithBaseURL(slackSrv.URL))
	n, err := New(Config{
		ExternalURL:  "https://factory.example",
		SlackChannel: "C0FACTORY",
		Slack:        slackClient,
		GitHub:       ghClient,
		Threads:      newMemThreads(),
		Now:          func() time.Time { return now },
		Sleep:        func(context.Context, time.Duration) error { return nil },
		Log:          logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	ev.SlackTeam = ""
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Slack: slackClient, GitHub: ghClient, ExternalURL: "https://factory.example"}
	logger.Info("notifier", "notifier", n, "config", cfg)
	blob := log.String() + fmt.Sprintf("%s %v %+v %#v %s %v %+v %#v", n, n, n, n, cfg, cfg, cfg, cfg)
	if strings.Contains(blob, ghCanary) || strings.Contains(blob, slackCanary) {
		t.Fatalf("token leaked: %s", blob)
	}
	if strings.Contains(log.String(), "outbound delivery stopped") {
		t.Fatalf("delivery failed: %s", log.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if slackPosts != 2 {
		t.Fatalf("slack posts %d paths %v", slackPosts, ghPaths)
	}
	joined := strings.Join(ghPaths, "\n")
	for _, want := range []string{"/installation", "/access_tokens", "/comments", "/check-runs"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
}
