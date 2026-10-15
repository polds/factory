package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestGitHubFakeLifecycle(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	const token = "ghs_testtokenvalue0123456789abcdef"
	srv, err := newServer(token, log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	installStatus, _ := doJSON(t, ts, http.MethodGet, "/repos/polds/factory/installation", "jwt-shaped", nil)
	if installStatus != http.StatusOK {
		t.Fatalf("installation %d", installStatus)
	}
	_, mintBody := doJSON(t, ts, http.MethodPost, "/app/installations/7/access_tokens", "jwt-shaped", map[string]any{
		"repositories": []string{"factory"},
		"permissions":  map[string]string{"contents": "write", "checks": "write"},
	})
	var minted struct {
		Token string `json:"token"`
	}
	decode(t, mintBody, &minted)
	if minted.Token != token {
		t.Fatal("mint did not return the installation token")
	}

	deniedStatus, deniedBody := doJSON(t, ts, http.MethodPost, "/repos/polds/factory/check-runs", "other", map[string]any{
		"head_sha": "abc", "status": "in_progress",
	})
	if deniedStatus != http.StatusUnauthorized || strings.Contains(string(deniedBody), token) {
		t.Fatalf("denied %d %s", deniedStatus, deniedBody)
	}

	_, createdBody := doJSON(t, ts, http.MethodPost, "/repos/polds/factory/check-runs", token, map[string]any{
		"head_sha": "abc123",
		"status":   "in_progress",
		"output":   map[string]string{"summary": "running"},
	})
	var check struct {
		ID int64 `json:"id"`
	}
	decode(t, createdBody, &check)
	if check.ID != 1 {
		t.Fatalf("check id %d", check.ID)
	}
	updatedStatus, _ := doJSON(t, ts, http.MethodPatch, "/repos/polds/factory/check-runs/1", token, map[string]any{
		"status": "completed", "conclusion": "success",
		"output": map[string]string{"summary": "done"},
	})
	if updatedStatus != http.StatusOK {
		t.Fatalf("update %d", updatedStatus)
	}

	_, commentBody := doJSON(t, ts, http.MethodPost, "/repos/polds/factory/issues/17/comments", token, map[string]string{
		"body": "<!-- factory:task_1 -->\nroot",
	})
	var posted struct {
		ID int64 `json:"id"`
	}
	decode(t, commentBody, &posted)
	patchedStatus, _ := doJSON(t, ts, http.MethodPatch, "/repos/polds/factory/issues/comments/1", token, map[string]string{
		"body": "<!-- factory:task_1 -->\nstatus",
	})
	if patchedStatus != http.StatusOK {
		t.Fatalf("comment patch %d", patchedStatus)
	}

	_, firstBody := doJSON(t, ts, http.MethodPost, "/_spike/commits", token, map[string]string{"message": "first"})
	var commit struct {
		Number  int    `json:"number"`
		Commits int    `json:"commits"`
		SHA     string `json:"sha"`
	}
	decode(t, firstBody, &commit)
	if commit.Number != 77 || commit.Commits != 1 || len(commit.SHA) != 64 {
		t.Fatalf("commit %+v", commit)
	}
	_, secondBody := doJSON(t, ts, http.MethodPost, "/_spike/commits", token, map[string]string{"message": "second"})
	decode(t, secondBody, &commit)
	if commit.Commits != 2 {
		t.Fatalf("commits %d", commit.Commits)
	}

	stateReq, err := http.NewRequest(http.MethodGet, ts.URL+"/_spike/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	stateRes, err := http.DefaultClient.Do(stateReq)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(stateRes.Body)
	_ = stateRes.Body.Close()
	if strings.Contains(string(raw), token) || strings.Contains(logs.String(), token) {
		t.Fatal("state or log contains the installation token")
	}
	var state stateDocument
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.TokenMints != 1 || len(state.Mints) != 1 || state.Mints[0].Repositories[0] != "factory" {
		t.Fatalf("mints %+v", state.Mints)
	}
	if len(state.Checks) != 2 || state.Checks[0].Op != "create" || state.Checks[0].Status != "in_progress" || state.Checks[1].Op != "update" || state.Checks[1].Conclusion != "success" {
		t.Fatalf("checks %+v", state.Checks)
	}
	if len(state.Pull.Commits) != 2 || state.Comments[0].Body != "<!-- factory:task_1 -->\nstatus" {
		t.Fatalf("pull %+v comments %+v", state.Pull, state.Comments)
	}
}

func TestGitHubFakeConcurrentCommits(t *testing.T) {
	srv, err := newServer("ghs_concurrenttokenvalue0123456789", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := json.Marshal(map[string]string{"message": "race"})
			if err != nil {
				errc <- err
				return
			}
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/_spike/commits", bytes.NewReader(raw))
			if err != nil {
				errc <- err
				return
			}
			req.Header.Set("Authorization", "Bearer ghs_concurrenttokenvalue0123456789")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				errc <- err
				return
			}
			defer func() { _ = res.Body.Close() }()
			_, _ = io.Copy(io.Discard, res.Body)
			if res.StatusCode != http.StatusCreated {
				errc <- errors.New("commit was not created")
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if got := srv.snapshot().Pull.Commits; len(got) != 8 {
		t.Fatalf("commits %d", len(got))
	}
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(raw))
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
	return res.StatusCode, out
}

func decode(t *testing.T, raw []byte, dest any) {
	t.Helper()
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatal(err)
	}
}
