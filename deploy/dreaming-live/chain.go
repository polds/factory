package dreaminglive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/server"
	"github.com/polds/factory/internal/store"
)

// ChainInput is one offline rehearsal of the live procedure.
type ChainInput struct {
	Server   *server.Server
	Store    *store.Store
	DSN      string
	BaseURL  string
	APIKey   string
	ModelURL string
	// ModelKey is the inference credential. Empty keeps the rehearsal key.
	ModelKey string
	Price    string
	Tree     definition.NativeTree
	RoleDir  string
	Ceiling  int64
	// Mode selects the stop: good, tiny, allowance, cancel, no-sentinel, compile.
	Mode string
	// SkipCancel is the mutation of the cancel rehearsal: the same chain
	// without CancelNativeSubtree. Publication and a second model call proceed.
	SkipCancel bool
}

// ChainResult is what the rehearsal observed.
type ChainResult struct {
	X           string
	Published   bool
	JoinRefused bool
	Spent       int64
}

// noteRehearsal runs at the start of the rehearsal chain. Live must not call
// RunChain. Tests replace this to prove mode=live did not select it.
var noteRehearsal = func() {}

type openedSeat struct {
	factoryUID string
	invID      string
	key        string
	token      string
	viktorRun  string
	rootTask   string
	rootRun    string
	agentID    string
}

// RunChain drives the offline rehearsal: an isolated bundle and evidence
// text supplied by the test. mode=live does not call it.
func RunChain(ctx context.Context, in ChainInput) (ChainResult, error) {
	noteRehearsal()
	seat, err := openViktor(ctx, in)
	if err != nil {
		return ChainResult{}, err
	}
	body := tenantTest(in.Mode != "no-sentinel")
	bundle, sha, err := gitBundle(body)
	if err != nil {
		return ChainResult{}, err
	}
	if err := postBundle(in.BaseURL, seat.viktorRun, seat.token, sha, bundle); err != nil {
		return ChainResult{}, err
	}
	oskar, err := in.Server.DelegateNative(ctx, seat.factoryUID, seat.rootTask, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: seat.key + "-oskar", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		return ChainResult{}, fmt.Errorf("delegate oskar: %w", err)
	}
	oskarAct, err := coordination.ClaimActivation(ctx, in.Store, oskar.ChildID, store.ClaimInput{AgentID: seat.agentID})
	if err != nil {
		return ChainResult{}, err
	}
	if _, err := in.Store.Lease(ctx, "default", time.Minute, 8); err != nil {
		return ChainResult{}, err
	}
	oskarExec, err := in.Store.StartExecution(ctx, oskarAct.RunID, "dreaming-worker", hashToken("oskar-"+seat.token))
	if err != nil {
		return ChainResult{}, err
	}
	red, green := evidenceTexts(in.Mode)
	events := []json.RawMessage{
		evidenceEvent(harness.EvidenceTestsFailed, red),
		evidenceEvent(harness.EvidenceTestsPassed, green),
	}
	if _, err := in.Store.AppendEvents(ctx, oskarAct.RunID, oskarExec.ID, 0, events); err != nil {
		return ChainResult{}, err
	}
	if _, err := in.Store.Lease(ctx, "default", time.Minute, 8); err != nil {
		return ChainResult{}, err
	}
	rootExec, err := in.Store.StartExecution(ctx, seat.rootRun, "dreaming-worker", hashToken("root-"+seat.token))
	if err != nil {
		return ChainResult{}, fmt.Errorf("start root: %w", err)
	}
	if _, err := in.Store.FinishExecution(ctx, rootExec.ID, store.Terminal{State: store.ExecCompleted}); err != nil {
		return ChainResult{}, err
	}
	// Evidence is in place before the model call so a 402 or a denied
	// reserve still reaches the publisher instead of failing the bundle post.
	_ = runSeat(ctx, in, seat.viktorRun, seat.token, 1)
	if in.Mode == "cancel" {
		if !in.SkipCancel {
			if _, err := in.Server.CancelNativeSubtree(ctx, seat.factoryUID, seat.rootTask, false); err != nil {
				return ChainResult{}, err
			}
		}
		_ = runSeat(ctx, in, seat.viktorRun, seat.token, 1)
	}
	_, completeErr := in.Server.CompleteNative(ctx, seat.factoryUID, seat.rootTask, false)
	published := completeErr == nil && draftOpened(ctx, in.DSN, seat.invID)
	return ChainResult{
		X: sha, Published: published, JoinRefused: true,
		Spent: spentCents(ctx, in.DSN, seat.invID),
	}, nil
}

func openViktor(ctx context.Context, in ChainInput) (openedSeat, error) {
	if in.Ceiling <= 0 {
		in.Ceiling = CeilingCents
	}
	if err := os.Setenv("FACTORY_ROLE_SOURCE", in.RoleDir); err != nil {
		return openedSeat{}, err
	}
	if err := os.Setenv("FACTORY_PUBLISH_DRAFT_REPO", DreamingRepo); err != nil {
		return openedSeat{}, err
	}
	if err := os.Setenv(harness.EnvEvidenceMutation, configuredMutation()); err != nil {
		return openedSeat{}, err
	}
	if err := os.Setenv(harness.EnvEvidencePackage, configuredPackage()); err != nil {
		return openedSeat{}, err
	}
	fac, err := in.Server.ListFactories(ctx)
	if err != nil || len(fac) == 0 {
		return openedSeat{}, fmt.Errorf("factories: %w", err)
	}
	factoryUID := fac[0].Name
	if factoryUID == "" {
		factoryUID = fac[0].ID
	}
	factoryID := fac[0].ID
	if in.Server != nil {
		in.Server.PutRoleCache(factoryID, in.Tree.SourceCommit, in.RoleDir)
	}
	agents, err := in.Store.ListAgents(ctx, factoryID)
	if err != nil || len(agents) == 0 {
		return openedSeat{}, fmt.Errorf("agents: %w", err)
	}
	goal, goalErr := LoadGoal()
	if goalErr != nil {
		return openedSeat{}, goalErr
	}
	item, err := in.Store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID, Source: store.SourceManual, Title: "DB-backed tenant-isolation tests",
		Body: goal, Stage: store.StagePlanning,
	})
	if err != nil {
		return openedSeat{}, err
	}
	key := "d6c-" + item.ID
	inv, err := in.Store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID: factoryID, ProfileName: "scan", SourceOccurrenceKey: key,
		CanonicalDigest: "digest-" + key, DefinitionDigest: "def", State: "accepted",
		Intent: "new", Priority: "interactive", WorkItemID: item.ID, NeutralStage: true,
		TargetRepository: DreamingRepo,
		PrincipalJSON:    json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !inv.Created {
		return openedSeat{}, fmt.Errorf("invocation: %w", err)
	}
	if err := pinInvocation(ctx, in.DSN, inv.Row.ID, in.Tree.SourceCommit); err != nil {
		return openedSeat{}, err
	}
	tree := in.Tree
	req := apiTree(tree, agents[0].ID, item.ID, in.Ceiling)
	opened, err := in.Server.OpenNativeTree(ctx, factoryUID, inv.Row.ID, req)
	if err != nil {
		return openedSeat{}, fmt.Errorf("open tree: %w", err)
	}
	if err := authorizeInvocation(ctx, in, factoryUID, inv.Row.ID); err != nil {
		return openedSeat{}, err
	}
	rootAct, err := coordination.ClaimActivation(ctx, in.Store, opened.RootTaskID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		return openedSeat{}, fmt.Errorf("claim root: %w", err)
	}
	beatriz, err := in.Server.DelegateNative(ctx, factoryUID, opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "beatriz-salgado", IdempotencyKey: key + "-beatriz", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		return openedSeat{}, fmt.Errorf("delegate beatriz: %w", err)
	}
	if _, err := coordination.ClaimActivation(ctx, in.Store, beatriz.ChildID, store.ClaimInput{AgentID: agents[0].ID}); err != nil {
		return openedSeat{}, err
	}
	viktor, err := in.Server.DelegateNative(ctx, factoryUID, beatriz.ChildID, api.NativeDelegateRequest{
		Assignee: "viktor-halas", IdempotencyKey: key + "-viktor", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		return openedSeat{}, fmt.Errorf("delegate viktor: %w", err)
	}
	viktorAct, err := coordination.ClaimActivation(ctx, in.Store, viktor.ChildID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		return openedSeat{}, err
	}
	if err := setTaskStatus(ctx, in.DSN, viktor.ChildID, store.NativeCompleted); err != nil {
		return openedSeat{}, err
	}
	if _, err := in.Server.JoinNative(ctx, factoryUID, beatriz.ChildID); err == nil {
		return openedSeat{}, fmt.Errorf("beatriz join succeeded without X")
	}
	if err := setTaskStatus(ctx, in.DSN, viktor.ChildID, store.NativeRunning); err != nil {
		return openedSeat{}, err
	}
	token := "tok-" + strings.ReplaceAll(key, ":", "")
	var startErr error
	for try := 0; try < 40; try++ {
		if _, startErr = in.Store.Lease(ctx, "default", time.Minute, 8); startErr != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if _, startErr = in.Store.StartExecution(ctx, viktorAct.RunID, "dreaming-worker", hashToken(token)); startErr == nil {
			break
		}
		if !strings.Contains(startErr.Error(), "queued") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if startErr != nil {
		return openedSeat{}, fmt.Errorf("start viktor: %w", startErr)
	}
	return openedSeat{
		factoryUID: factoryUID, invID: inv.Row.ID, key: key, token: token,
		viktorRun: viktorAct.RunID, rootTask: opened.RootTaskID, rootRun: rootAct.RunID,
		agentID: agents[0].ID,
	}, nil
}

func draftOpened(ctx context.Context, dsn, invocationID string) bool {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close(ctx) }()
	var state string
	err = conn.QueryRow(ctx, `SELECT state FROM draft_publications WHERE invocation_id = $1`, invocationID).Scan(&state)
	return err == nil && state == string(store.DraftOpened)
}

func spentCents(ctx context.Context, dsn, invocationID string) int64 {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return -1
	}
	defer func() { _ = conn.Close(ctx) }()
	var spent int64
	if err := conn.QueryRow(ctx, `SELECT COALESCE(SUM(spent_cents), 0) FROM budget_scopes WHERE invocation_id = $1`, invocationID).Scan(&spent); err != nil {
		return -1
	}
	return spent
}

func setTaskStatus(ctx context.Context, dsn, taskID, status string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	tag, err := conn.Exec(ctx, `UPDATE native_tasks SET task_status = $2 WHERE id = $1`, taskID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("task status %s", taskID)
	}
	return nil
}

func pinInvocation(ctx context.Context, dsn, invocationID, pin string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, `UPDATE invocations SET pinned_head_sha = $2, target_repository = $3 WHERE id = $1`, invocationID, pin, DreamingRepo)
	return err
}

func authorizeInvocation(ctx context.Context, in ChainInput, factoryUID, invocationID string) error {
	conn, err := pgx.Connect(ctx, in.DSN)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	scopeID := "invocation:" + invocationID
	if _, err := conn.Exec(ctx, `
		INSERT INTO budget_scopes (scope_id, invocation_id, kind, ceiling_cents)
		VALUES ($1, $2, 'invocation', $3)
		ON CONFLICT (scope_id) DO NOTHING`, scopeID, invocationID, in.Ceiling); err != nil {
		return err
	}
	if _, err := in.Server.AuthorizeNativeCeiling(ctx, factoryUID, api.NativeCeilingRequest{
		ScopeID: scopeID, NewCeiling: in.Ceiling,
	}); err != nil {
		return fmt.Errorf("ceiling: %w", err)
	}
	return nil
}

func apiTree(tree definition.NativeTree, agentID, itemID string, ceiling int64) api.NativeTreeRequest {
	personas := make([]api.NativePersona, len(tree.Personas))
	for i, persona := range tree.Personas {
		files := make([]api.NativeRoleFile, len(persona.Files))
		for j, file := range persona.Files {
			files[j] = api.NativeRoleFile{
				Name: file.Name, SHA256: file.SHA256, GitBlob: file.GitBlob,
				Bytes: append([]byte{}, file.Bytes...),
			}
		}
		personas[i] = api.NativePersona{
			Name: persona.Name, ReportsTo: persona.ReportsTo, SpawnableBy: append([]string{}, persona.SpawnableBy...),
			Role: persona.Role, SourceReportsTo: persona.SourceReportsTo, Files: files,
		}
	}
	return api.NativeTreeRequest{
		Personas: personas, MaxDepth: tree.MaxDepth, AbsoluteMaxDepth: tree.AbsoluteMaxDepth,
		AllowAncestorShortcut: tree.AllowAncestorShortcut, Delegation: tree.Delegation,
		Synthetic: tree.Synthetic, NativeHierarchy: tree.NativeHierarchy,
		CeilingCents: ceiling, Slots: 4, MaxLive: 4,
		AgentID: agentID, WorkItemID: itemID, Stage: store.StagePlanning,
		SourceCommit: tree.SourceCommit, RosterSHA256: tree.RosterSHA256,
		RosterGitBlob: tree.RosterGitBlob, BundleMode: tree.BundleMode,
		Evidence: &api.NativeEvidenceRequest{
			Package:  configuredPackage(),
			Mutation: configuredMutation(),
		},
	}
}

func runSeat(ctx context.Context, in ChainInput, runID, token string, turns int) int {
	work, err := os.MkdirTemp("", "dreaming-seat-")
	if err != nil {
		return 1
	}
	defer func() { _ = os.RemoveAll(work) }()
	rt, err := os.MkdirTemp("", "dreaming-rt-")
	if err != nil {
		return 1
	}
	defer func() { _ = os.RemoveAll(rt) }()
	modelKey := in.ModelKey
	if modelKey == "" {
		modelKey = "sk-offline-rehearsal"
	}
	return harness.Run(ctx, harness.Options{
		Args: []string{"--task-id", runID, "--model", ModelID},
		Env: []string{
			"OPENAI_BASE_URL=" + in.ModelURL,
			"OPENAI_API_KEY=" + modelKey,
			"FACTORY_RUN_TOKEN=" + token,
			harness.EnvSpendURL + "=" + in.BaseURL + "/api/v1/agent/runs/" + runID + "/spend",
			harness.EnvSpendPrice + "=" + in.Price,
			"FACTORY_TASK_PROMPT=Write the tenant isolation test and commit it locally.",
			"FACTORY_MAX_TURNS=" + fmt.Sprintf("%d", turns),
			harness.EnvKeepDumpable + "=1",
		},
		Workspace:   work,
		RuntimeDir:  rt,
		Stderr:      io.Discard,
		ChatTimeout: 15 * time.Second,
	})
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func tenantTest(sentinel bool) string {
	marker := "factoryEvidencePass"
	if !sentinel {
		marker = "otherMarker"
	}
	return "package oracle\n\nimport \"testing\"\n\nfunc TestTenantIsolation(t *testing.T) {\n\tif tenant() != \"" + marker + "\" {\n\t\tt.Fatal(\"tenant\")\n\t}\n}\n\nfunc tenant() string { return \"" + marker + "\" }\n"
}

func evidenceTexts(mode string) (string, string) {
	red := "{\"Action\":\"fail\",\"Package\":\"oracle\",\"Test\":\"TestTenantIsolation\"}\n"
	green := "{\"Action\":\"pass\",\"Package\":\"oracle\",\"Test\":\"TestTenantIsolation\"}\n"
	if mode == "compile" {
		red = "# oracle\noracle/tenant_test.go:1:1: undefined: missing\nFAIL\toracle [build failed]\n"
	}
	return red, green
}

func evidenceEvent(status, text string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{"type": harness.EventEvidenceTest, "status": status, "text": text})
	if err != nil {
		return json.RawMessage(`{"type":"evidence.test"}`)
	}
	return raw
}

func gitBundle(body string) ([]byte, string, error) {
	dir, err := os.MkdirTemp("", "dreaming-bundle-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := git(dir, "init"); err != nil {
		return nil, "", err
	}
	if err := git(dir, "config", "user.email", "dreaming@example.com"); err != nil {
		return nil, "", err
	}
	if err := git(dir, "config", "user.name", "dreaming"); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "oracle", "tenant_test.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, "", err
	}
	if err := git(dir, "add", "oracle/tenant_test.go"); err != nil {
		return nil, "", err
	}
	if err := git(dir, "commit", "-m", "tenant isolation"); err != nil {
		return nil, "", err
	}
	sha, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", err
	}
	bundle := filepath.Join(dir, "head.bundle")
	if err := git(dir, "bundle", "create", bundle, "HEAD"); err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(bundle)
	if err != nil {
		return nil, "", err
	}
	return raw, sha, nil
}

func postBundle(base, runID, token, sha string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/agent/runs/"+runID+"/head.bundle", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Factory-Head", sha)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("bundle %d %s", res.StatusCode, msg)
	}
	return nil
}
