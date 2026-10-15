package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlackFakeThread(t *testing.T) {
	var logs bytes.Buffer
	const token = "xoxb-slack-fake-token-value"
	srv := newSlack(token, slog.New(slog.NewTextHandler(&logs, nil)))
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	deniedBody := postSlack(t, ts, "other-token", "/chat.postMessage", map[string]string{"channel": "C0", "text": "nope"})
	if !strings.Contains(deniedBody, `"ok":false`) || strings.Contains(deniedBody, token) || strings.Contains(deniedBody, "other-token") {
		t.Fatalf("denied %s", deniedBody)
	}

	rootBody := postSlack(t, ts, token, "/chat.postMessage", map[string]string{"channel": "C0M4FACTORY", "text": "root"})
	var root struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(rootBody), &root); err != nil || !root.OK || root.TS == "" {
		t.Fatalf("root %+v", root)
	}
	statusBody := postSlack(t, ts, token, "/chat.postMessage", map[string]string{
		"channel": "C0M4FACTORY", "thread_ts": root.TS, "text": "status",
	})
	var status struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(statusBody), &status); err != nil || status.TS == "" || status.TS == root.TS {
		t.Fatalf("status %+v", status)
	}
	updated := postSlack(t, ts, token, "/chat.update", map[string]string{
		"channel": "C0M4FACTORY", "ts": status.TS, "text": "status edited",
	})
	if !strings.Contains(updated, `"ok":true`) {
		t.Fatal("update failed")
	}
	_ = postSlack(t, ts, token, "/chat.postMessage", map[string]string{
		"channel": "C0M4FACTORY", "thread_ts": root.TS, "text": "Pull request opened: https://github.com/polds/factory/pull/77",
	})

	raw := getBody(t, ts.URL+"/_spike/state")
	if strings.Contains(raw, token) || strings.Contains(logs.String(), token) {
		t.Fatal("state or log contains the bot token")
	}
	var state slackState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	posts, updates := 0, 0
	rootSeen := false
	milestone := false
	for _, row := range state.Messages {
		switch row.Op {
		case "post":
			posts++
			if row.ThreadTS == "" {
				rootSeen = true
			}
			if strings.Contains(row.Text, "Pull request opened:") {
				milestone = true
			}
		case "update":
			updates++
			if row.Text != "status edited" {
				t.Fatalf("update text %q", row.Text)
			}
		default:
			t.Fatalf("op %s", row.Op)
		}
	}
	if posts != 3 || updates != 1 || !rootSeen || !milestone {
		t.Fatalf("posts %d updates %d root %v milestone %v", posts, updates, rootSeen, milestone)
	}
}

func postSlack(t *testing.T, ts *httptest.Server, token, path string, body map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
