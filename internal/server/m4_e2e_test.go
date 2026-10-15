package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/protocol"
)

const (
	e2eWebhook   = "whsec_e2e_value"
	e2eBot       = "xoxb-e2e-token"
	e2eSlack     = "slack-e2e-signing"
	e2eMinted    = "ghs_e2e_token_value"
	e2ePR        = "https://github.com/acme/payments-service/pull/77"
	e2eTeam      = "T0FACTORY"
	e2eChannel   = "C0FACTORY"
	e2eLinear    = "lin_whsec_e2e"
	e2eLinearKey = "lin_api_e2e"
	e2eIssue     = "lin_issue_1"
	e2eHead      = "abc123"
)

func TestE2EM4(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	gh := newGitHubFake(t, e2eMinted)
	slack := newSlackFake(t)
	lin := newLinearFake(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	env := boot(t, bootOpts{
		definition: writeM4Definition,
		configure: func(cfg *Config) {
			cfg.ExternalURL = "http://factory.example"
			cfg.GitHubAppID = 99
			cfg.GitHubAppKey = key
			cfg.GitHubWebhookSecret = e2eWebhook
			cfg.GitHubTokenMode = tokenModeApp
			cfg.GitHubAPIBase = gh.URL
			cfg.SlackBotToken = e2eBot
			cfg.SlackSigningSecret = e2eSlack
			cfg.SlackTeamID = e2eTeam
			cfg.SlackAPIBase = slack.URL
			cfg.LinearWebhookSecret = e2eLinear
			cfg.LinearAPIKey = e2eLinearKey
			cfg.LinearAPIBase = lin.URL
			cfg.Secrets = map[string]string{"GH_TOKEN": "planted-not-used", "NPM_TOKEN": plantedNPM}
			cfg.OutboundSleep = func(context.Context, time.Duration) error { return nil }
		},
	})
	if strings.Contains(env.logs.String(), e2eWebhook) || strings.Contains(env.logs.String(), e2eBot) || strings.Contains(env.logs.String(), e2eSlack) || strings.Contains(env.logs.String(), e2eLinear) || strings.Contains(env.logs.String(), e2eLinearKey) {
		t.Fatalf("startup log contains a secret: %s", env.logs.String())
	}

	worker := dialWorker(t, env, "local-1")
	defer worker.close()

	postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-issue-1", labeledIssue())

	first, ok := worker.readAssignmentWithin(t, 15*time.Second)
	if !ok {
		t.Fatal("no triage assignment")
	}
	if first.EnvVars["GH_TOKEN"] != e2eMinted {
		t.Fatalf("GH_TOKEN %q", first.EnvVars["GH_TOKEN"])
	}
	if first.EnvVars["FACTORY_STAGE"] != "triage" {
		t.Fatalf("stage %s", first.EnvVars["FACTORY_STAGE"])
	}
	conv := first.EnvVars["FACTORY_CONVERSATION_ID"]
	if conv == "" {
		t.Fatal("missing conversation")
	}
	if !gh.sawRepo("payments-service") {
		t.Fatalf("token repos %v", gh.repos())
	}
	finishAssignment(t, worker, first)

	second, ok := worker.readAssignmentWithin(t, 15*time.Second)
	if !ok {
		t.Fatal("no building assignment")
	}
	if second.EnvVars["FACTORY_STAGE"] != "building" || second.EnvVars["FACTORY_CONVERSATION_ID"] != conv {
		t.Fatalf("building stage %s conversation %s", second.EnvVars["FACTORY_STAGE"], second.EnvVars["FACTORY_CONVERSATION_ID"])
	}
	finishAssignment(t, worker, second)
	waitWorkItem(t, env, "human_handoff", "complete")

	postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "pull_request", "delivery-pr-1", openedPull())
	slack.waitFor(t, e2ePR)
	slack.waitFor(t, "implementer")
	slack.waitFor(t, "test-model")
	gh.waitCheck(t, e2eHead, "implementer", "test-model")
	root := slack.rootTS()
	if root == "" {
		t.Fatal("slack root missing")
	}
	ref := waitSlackRef(t, env)
	if !strings.Contains(ref, "slack:"+e2eTeam+"/"+e2eChannel+"/") {
		t.Fatalf("slack ref %s", ref)
	}

	postSlack(t, env.base+"/api/v1/ingress/slack", e2eSlack, slackReply(root))
	third, ok := worker.readAssignmentWithin(t, 15*time.Second)
	if !ok {
		t.Fatal("no continuation assignment")
	}
	if third.EnvVars["FACTORY_CONVERSATION_ID"] != conv || third.EnvVars["FACTORY_STAGE"] != "building" {
		t.Fatalf("continuation stage %s conversation %s want %s", third.EnvVars["FACTORY_STAGE"], third.EnvVars["FACTORY_CONVERSATION_ID"], conv)
	}

	postLinear(t, env.base+"/api/v1/ingress/linear", e2eLinear, "delivery-lin-1", linearIssue())
	lin.waitFor(t, e2eIssue, "implementer", "test-model")
	env.srv.integ.notifier.Settle()
	if strings.Contains(env.logs.String(), e2eMinted) || strings.Contains(env.logs.String(), e2eWebhook) || strings.Contains(env.logs.String(), e2eBot) || strings.Contains(env.logs.String(), e2eLinearKey) {
		t.Fatalf("log contains a secret: %s", env.logs.String())
	}
}

func writeM4Definition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, root+"/factory.yaml", `
schemaVersion: v1alpha1
name: m4
secrets:
  - GH_TOKEN
agentDefaults:
  model: auto
  runner: local
  pool: standard
integrations:
  - type: slack
    slack:
      autoRespondToThreadReplies: true
      channel: C0FACTORY
      allowedUserIds:
        - U0HUMAN
pipeline:
  stages: [triage, building]
  agents:
    triage: implementer
    building: implementer
  approvals:
    spec: { required: false }
    handoff: { required: false }
`)
	mustWrite(t, root+"/runners/local.yaml", `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)
	mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
maxTurns: 7
---

Do the task.
`)
	mustWrite(t, root+"/automations/labeled-intake/automation.md", `
---
enabled: true
agent: implementer
triggers:
  - provider: github
    event: issue_labeled
    filter:
      repos: [acme/payments-service]
      labels: [factory-ready]
---

Take the labeled issue through the factory.
`)
	mustWrite(t, root+"/automations/linear-intake/automation.md", `
---
enabled: true
agent: implementer
triggers:
  - provider: linear
    event: issue_created
---

Take the Linear issue through the factory.
`)
	return root
}

func TestE2EM4DurableBinding(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	t.Run("restart", func(t *testing.T) {
		gh, key := narrowGitHub(t)
		env := bootNarrow(t, gh, key)
		postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-narrow-restart", labeledIssue())
		waitIntake(t, env, "labeled-intake")
		if err := env.srv.Close(); err != nil {
			t.Fatal(err)
		}
		peer := openPeer(t, env)
		worker := dialWorker(t, peer, "narrow-1")
		defer worker.close()
		asg := readNarrowAssignment(t, worker, gh, peer)
		if started(peer.srv, asg.TaskID) {
			t.Fatal("assigning process recorded the intake outcome")
		}
	})
	t.Run("nonleader", func(t *testing.T) {
		gh, key := narrowGitHub(t)
		leader := bootNarrow(t, gh, key)
		worker := dialWorker(t, leader, "narrow-leader")
		defer worker.close()
		silenceDispatch(leader.srv)
		time.Sleep(400 * time.Millisecond)
		peer := openPeer(t, leader)
		postGitHub(t, peer.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-narrow-peer", labeledIssue())
		asg := readNarrowAssignment(t, worker, gh, leader)
		if started(leader.srv, asg.TaskID) {
			t.Fatal("scheduler leader recorded the intake outcome")
		}
		if !started(peer.srv, asg.TaskID) {
			t.Fatal("non-leader did not record the intake outcome")
		}
	})
}

func narrowGitHub(t *testing.T) (*gitHubFake, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return newGitHubFake(t, e2eMinted), key
}

func bootNarrow(t *testing.T, gh *gitHubFake, key *rsa.PrivateKey) *env {
	t.Helper()
	return boot(t, bootOpts{
		definition: writeNarrowDefinition,
		configure: func(cfg *Config) {
			cfg.GitHubAppID = 99
			cfg.GitHubAppKey = key
			cfg.GitHubWebhookSecret = e2eWebhook
			cfg.GitHubTokenMode = tokenModeApp
			cfg.GitHubAPIBase = gh.URL
			cfg.Secrets = map[string]string{"GH_TOKEN": "planted-not-used", "NPM_TOKEN": plantedNPM}
			cfg.OutboundSleep = func(context.Context, time.Duration) error { return nil }
		},
	})
}

func writeNarrowDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, root+"/factory.yaml", `
schemaVersion: v1alpha1
name: m4
secrets:
  - GH_TOKEN
agentDefaults:
  model: auto
  runner: local
  pool: standard
pipeline:
  stages: [triage, building]
  agents:
    triage: implementer
    building: implementer
  approvals:
    spec: { required: false }
    handoff: { required: false }
`)
	mustWrite(t, root+"/runners/local.yaml", `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)
	mustWrite(t, root+"/runners/narrow.yaml", `
description: Narrow docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-narrow:dev
`)
	mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
  - GH_TOKEN
---

Do the task.
`)
	mustWrite(t, root+"/automations/labeled-intake/automation.md", `
---
enabled: true
agent: implementer
runner: narrow
secrets:
  - GH_TOKEN
required_repos:
  - { codeForge: GITHUB, owner: acme, name: ledger }
triggers:
  - provider: github
    event: issue_labeled
    filter:
      repos: [acme/payments-service]
      labels: [factory-ready]
---

Take the labeled issue through the factory.
`)
	return root
}

func openPeer(t *testing.T, src *env) *env {
	t.Helper()
	cfg := src.srv.cfg
	logs := &syncLog{}
	cfg.Log = newTestLogger(logs)
	srv, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	srv.cfg.TaskServerURL = ts.URL
	return &env{t: t, srv: srv, base: ts.URL, apiKey: cfg.APIKey, workerKey: cfg.WorkerAPIKey, logs: logs, raw: src.raw}
}

func silenceDispatch(s *Server) {
	s.integ.mu.Lock()
	s.integ.dispatcher = nil
	s.integ.mu.Unlock()
}

func started(s *Server, runID string) bool {
	s.integ.mu.Lock()
	defer s.integ.mu.Unlock()
	_, ok := s.integ.started[runID]
	return ok
}

func waitIntake(t *testing.T, env *env, automation string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status, name, runID string
		err := env.raw.QueryRow(context.Background(), `
			SELECT r.status, s.automation, s.run_id
			FROM runs r
			JOIN intake_slots s ON s.run_id = r.id
			LIMIT 1`).Scan(&status, &name, &runID)
		if err == nil && status == "queued" && name == automation && runID != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("intake run was not queued")
}

func readNarrowAssignment(t *testing.T, worker *wsWorker, gh *gitHubFake, env *env) protocol.TaskAssignment {
	t.Helper()
	asg, ok := worker.readAssignmentWithin(t, 15*time.Second)
	if !ok {
		t.Fatalf("no assignment\n%s", env.logs.String())
	}
	if asg.DockerImage != "factory-narrow:dev" {
		t.Fatalf("image %s", asg.DockerImage)
	}
	if asg.EnvVars["GH_TOKEN"] != e2eMinted {
		t.Fatalf("GH_TOKEN %q", asg.EnvVars["GH_TOKEN"])
	}
	if _, ok := asg.EnvVars["NPM_TOKEN"]; ok {
		t.Fatal("narrowed secret NPM_TOKEN was sent")
	}
	if !gh.sawRepo("payments-service") || !gh.sawRepo("ledger") {
		t.Fatalf("token repos %v", gh.repos())
	}
	return asg
}

func finishAssignment(t *testing.T, w *wsWorker, asg protocol.TaskAssignment) {
	t.Helper()
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "done"})
}

func waitWorkItem(t *testing.T, env *env, stage, status string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var gotStage, gotStatus string
	for time.Now().Before(deadline) {
		err := env.raw.QueryRow(context.Background(), `SELECT stage, status FROM work_items LIMIT 1`).Scan(&gotStage, &gotStatus)
		if err == nil && gotStage == stage && gotStatus == status {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("work item %s/%s, want %s/%s", gotStage, gotStatus, stage, status)
}

func waitSlackRef(t *testing.T, env *env) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var ref string
	for time.Now().Before(deadline) {
		err := env.raw.QueryRow(context.Background(), `SELECT external_ref FROM external_refs WHERE provider = 'slack' LIMIT 1`).Scan(&ref)
		if err == nil && ref != "" {
			return ref
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("slack external ref missing")
	return ""
}

func postGitHub(t *testing.T, url, secret, event, delivery string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("github ingress %d %s", res.StatusCode, raw)
	}
}

func postLinear(t *testing.T, url, secret, delivery string, body []byte) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Linear-Delivery", delivery)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("linear ingress %d %s", res.StatusCode, raw)
	}
}

func postSlack(t *testing.T, url, secret string, body []byte) {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + ts + ":"))
	_, _ = mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("slack ingress %d %s", res.StatusCode, raw)
	}
}

func labeledIssue() []byte {
	return mustJSON(map[string]any{
		"action": "labeled",
		"label":  map[string]any{"name": "factory-ready"},
		"issue": map[string]any{
			"id": 4120, "number": 412, "title": "Add refunds", "body": "Please refund.",
			"html_url":   "https://github.com/acme/payments-service/issues/412",
			"updated_at": "2026-01-02T03:05:06Z",
			"labels":     []any{map[string]any{"name": "bug"}, map[string]any{"name": "factory-ready"}},
		},
		"repository": ghRepo(),
		"sender":     map[string]any{"login": "octocat", "id": 1, "type": "User"},
	})
}

func openedPull() []byte {
	return mustJSON(map[string]any{
		"action": "opened",
		"number": 77,
		"pull_request": map[string]any{
			"id": 7700, "number": 77, "title": "Refunds", "body": "Closes #412",
			"html_url": e2ePR, "merged": false, "updated_at": "2026-01-02T05:00:00Z",
			"head": map[string]any{"ref": "refunds", "sha": e2eHead},
			"base": map[string]any{"ref": "main"},
		},
		"repository": ghRepo(),
		"sender":     map[string]any{"login": "octocat", "id": 1, "type": "User"},
	})
}

func linearIssue() []byte {
	return mustJSON(map[string]any{
		"action":           "create",
		"type":             "Issue",
		"webhookTimestamp": time.Now().UnixMilli(),
		"createdAt":        "2026-01-02T06:00:00Z",
		"url":              "https://linear.app/acme/issue/LIN-1",
		"actor":            map[string]any{"id": "user_1", "name": "Ada"},
		"data": map[string]any{
			"id": e2eIssue, "number": 1, "title": "Linear refunds",
			"description": "from linear", "identifier": "LIN-1",
			"url": "https://linear.app/acme/issue/LIN-1",
		},
	})
}

func ghRepo() map[string]any {
	return map[string]any{
		"id": 100, "name": "payments-service", "full_name": "acme/payments-service",
		"owner": map[string]any{"login": "acme", "id": 10, "type": "Organization"},
	}
}

func slackReply(rootTS string) []byte {
	return mustJSON(map[string]any{
		"type":       "event_callback",
		"team_id":    e2eTeam,
		"event_id":   "EvREPLY1",
		"event_time": time.Now().Unix(),
		"event": map[string]any{
			"type": "message", "user": "U0HUMAN", "text": "ship the follow-up",
			"ts": "1600000099.000200", "thread_ts": rootTS, "channel": e2eChannel,
			"channel_type": "channel", "event_ts": "1600000099.000200",
		},
	})
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

type ghCheck struct {
	sha     string
	summary string
}

type gitHubFake struct {
	URL      string
	token    string
	mu       sync.Mutex
	seen     []string
	checks   []ghCheck
	comments []string
}

func newGitHubFake(t *testing.T, token string) *gitHubFake {
	t.Helper()
	f := &gitHubFake{token: token}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *gitHubFake) serve(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/installation"):
		writeJSON(w, map[string]any{"id": 7})
	case r.Method == http.MethodPost && strings.Contains(path, "/access_tokens"):
		var req struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.seen = append(f.seen, req.Repositories...)
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"token":      f.token,
			"expires_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
		})
	case r.Method == http.MethodGet && strings.Contains(path, "/comments"):
		writeJSON(w, []any{})
	case r.Method == http.MethodPost && strings.Contains(path, "/comments"):
		var comment struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &comment)
		f.mu.Lock()
		f.comments = append(f.comments, comment.Body)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"id": 1})
	case r.Method == http.MethodPatch && strings.Contains(path, "/comments/"):
		writeJSON(w, map[string]any{"id": 1})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/check-runs"):
		var check struct {
			HeadSHA string `json:"head_sha"`
			Output  struct {
				Summary string `json:"summary"`
			} `json:"output"`
		}
		_ = json.Unmarshal(body, &check)
		f.mu.Lock()
		f.checks = append(f.checks, ghCheck{sha: check.HeadSHA, summary: check.Output.Summary})
		id := len(f.checks)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"id": id})
	case r.Method == http.MethodPatch && strings.Contains(path, "/check-runs/"):
		writeJSON(w, map[string]any{"id": 1})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *gitHubFake) sawRepo(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, repo := range f.seen {
		if repo == name || strings.HasSuffix(repo, "/"+name) {
			return true
		}
	}
	return false
}

func (f *gitHubFake) repos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *gitHubFake) waitCheck(t *testing.T, sha string, needles ...string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, check := range f.checks {
			if check.sha != sha {
				continue
			}
			ok := true
			for _, needle := range needles {
				if !strings.Contains(check.summary, needle) {
					ok = false
					break
				}
			}
			if ok {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("check run %s missing in %+v", sha, f.checks)
}

type slackPost struct {
	channel string
	thread  string
	text    string
	ts      string
}

type slackFake struct {
	URL   string
	mu    sync.Mutex
	posts []slackPost
	n     int
}

func newSlackFake(t *testing.T) *slackFake {
	t.Helper()
	f := &slackFake{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *slackFake) serve(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, _ := io.ReadAll(r.Body)
	var msg struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Text     string `json:"text"`
		TS       string `json:"ts"`
	}
	_ = json.Unmarshal(body, &msg)
	f.mu.Lock()
	f.n++
	ts := msg.TS
	if ts == "" {
		ts = "1700000000." + strconv.Itoa(f.n)
	}
	if r.URL.Path == "/chat.postMessage" {
		f.posts = append(f.posts, slackPost{channel: msg.Channel, thread: msg.ThreadTS, text: msg.Text, ts: ts})
	}
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "ts": ts})
}

func (f *slackFake) waitFor(t *testing.T, needle string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, post := range f.posts {
			if strings.Contains(post.text, needle) {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("slack missing %q in %+v", needle, f.posts)
}

func (f *slackFake) rootTS() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, post := range f.posts {
		if post.thread == "" {
			return post.ts
		}
	}
	return ""
}

type linearComment struct {
	issue string
	body  string
}

type linearFake struct {
	URL      string
	mu       sync.Mutex
	comments []linearComment
}

func newLinearFake(t *testing.T) *linearFake {
	t.Helper()
	f := &linearFake{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *linearFake) serve(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			Input struct {
				IssueID string `json:"issueId"`
				Body    string `json:"body"`
			} `json:"input"`
		} `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	if strings.Contains(req.Query, "commentCreate") {
		f.mu.Lock()
		f.comments = append(f.comments, linearComment{issue: req.Variables.Input.IssueID, body: req.Variables.Input.Body})
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "cmt_1"}},
			},
		})
		return
	}
	if strings.Contains(req.Query, "commentUpdate") {
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"commentUpdate": map[string]any{"success": true, "comment": map[string]any{"id": "cmt_1"}},
			},
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *linearFake) waitFor(t *testing.T, issue string, needles ...string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, comment := range f.comments {
			if comment.issue != issue {
				continue
			}
			ok := true
			for _, needle := range needles {
				if !strings.Contains(comment.body, needle) {
					ok = false
					break
				}
			}
			if ok {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("linear comment %s missing in %+v", issue, f.comments)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
