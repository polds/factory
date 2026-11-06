package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/client"
)

func TestNegotiatePinnedHelp(t *testing.T) {
	help := PinnedClaudeHelp()
	fake := readRepo(t, "spike/lib/fakes/claude/help.txt")
	if help != fake {
		t.Fatal("pinned help and fake help differ")
	}
	caps, err := NegotiatePinned(help)
	if err != nil {
		t.Fatal(err)
	}
	if caps.NegotiatedSteer {
		t.Fatal("steer was negotiated without a proof")
	}
	if !caps.StreamJSON || !caps.Resume {
		t.Fatalf("pinned flags %+v", caps)
	}
	if !caps.InternalWorkersInSession || caps.InternalWorkersAreRoster || !caps.WorkflowMainLoopOnly {
		t.Fatalf("workflow policy %+v", caps)
	}
	withSteer := help + "\n      --steer\n"
	proven, err := Negotiate(withSteer, Proven{Steer: true, StreamJSON: true, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if !proven.NegotiatedSteer {
		t.Fatal("a proven steer flag was dropped")
	}
	unproven, err := NegotiatePinned(withSteer)
	if err != nil || unproven.NegotiatedSteer {
		t.Fatalf("production pin negotiated steer: %+v %v", unproven, err)
	}
	if _, err := Negotiate("   ", Proven{}); err == nil {
		t.Fatal("empty help was accepted")
	}
}

func TestIssueRunTokenDeniesElevation(t *testing.T) {
	ctx := context.Background()
	scope := TokenScope{
		FactoryID:    "fac_1",
		TaskID:       "ntask_1",
		InvocationID: "inv_1",
		Role:         RoleLead,
		Grants:       []string{GrantInboxRead, GrantAckWrite, GrantRosterDispatch, GrantTaskRead},
		BudgetCents:  100,
	}
	child, err := IssueRunToken(ctx, scope, Attenuation{
		Role:        RoleSpecialist,
		Grants:      []string{GrantInboxRead, GrantRosterDispatch},
		BudgetCents: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.HasGrant(GrantRosterDispatch) {
		t.Fatal("specialist token kept roster.dispatch")
	}
	if child.BudgetCents() != 40 || child.TaskID() != scope.TaskID || child.InvocationID() != scope.InvocationID {
		t.Fatalf("child scope task %s invocation %s budget %d", child.TaskID(), child.InvocationID(), child.BudgetCents())
	}
	if !strings.HasPrefix(child.ID(), "ntok_") {
		t.Fatal("token id is not ntok_")
	}
	secret := child.Value()
	printed := fmt.Sprintf("%v %+v %#v %s", child, child, &child, child.ID())
	raw, err := json.Marshal(child)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	slog.New(slog.NewTextHandler(&log, nil)).Info("token", "token", child)
	for _, blob := range []string{printed, string(raw), log.String()} {
		if strings.Contains(blob, secret) {
			t.Fatal("run token secret leaked")
		}
	}
	if _, err := IssueRunToken(ctx, scope, Attenuation{Grants: []string{GrantTaskWrite}}); err == nil || !strings.Contains(err.Error(), "grant_rejected") {
		t.Fatalf("elevation err %v", err)
	}
	if _, err := IssueRunToken(ctx, scope, Attenuation{BudgetCents: 101}); err == nil || !strings.Contains(err.Error(), "budget_exceeded") {
		t.Fatalf("budget err %v", err)
	}
	if _, err := IssueRunToken(ctx, scope, Attenuation{TaskID: "ntask_other"}); err == nil || !strings.Contains(err.Error(), "cross") {
		t.Fatalf("cross task err %v", err)
	}
	if _, err := IssueRunToken(ctx, scope, Attenuation{InvocationID: "inv_other"}); err == nil || !strings.Contains(err.Error(), "cross") {
		t.Fatalf("cross invocation err %v", err)
	}
	tool, err := IssueRunToken(ctx, scope, Attenuation{Role: RoleTool, Grants: []string{GrantInboxRead, GrantRosterDispatch}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(tool, GrantRosterDispatch, tool.TaskID(), tool.InvocationID()); err == nil {
		t.Fatal("tool token dispatched")
	}
	if err := Authorize(child, GrantInboxRead, "ntask_other", child.InvocationID()); err == nil {
		t.Fatal("cross-task authorize succeeded")
	}
	expired := child
	expired.expires = time.Now().Add(-time.Second)
	if err := Authorize(expired, GrantInboxRead, expired.TaskID(), expired.InvocationID()); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestMapClaudeStreamShapes(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		want      int
		status    string
		usage     bool
		malformed bool
		resume    string
	}{
		{name: "empty text", body: `{"type":"assistant","text":"","call_id":"c1"}` + "\n", want: 1, status: "ok"},
		{name: "empty tool", body: `{"type":"tool_result","tool":"bash","output":"","ok":true,"call_id":"c2"}` + "\n", want: 1, status: "ok"},
		{name: "refusal", body: `{"type":"refusal","text":"no","call_id":"c3"}` + "\n", want: 1, status: "refusal"},
		{name: "tool error", body: `{"type":"tool_result","tool":"bash","ok":false,"error":"boom","call_id":"c4"}` + "\n", want: 1, status: "tool_error"},
		{name: "malformed", body: "{\"type\":\"assistant\",\"text\":\"ok\",\"call_id\":\"c5\"}\n{not-json\n", want: 2, malformed: true},
		{name: "partial", body: `{"type":"assistant","text":"half","partial":true,"call_id":"c6"}` + "\n", want: 1, status: "partial"},
		{name: "401", body: `{"type":"error","status":401,"call_id":"c7"}` + "\n", want: 1, status: "401"},
		{name: "400", body: `{"type":"error","status":400,"call_id":"c8"}` + "\n", want: 1, status: "400"},
		{name: "429", body: `{"type":"error","status":429,"call_id":"c9"}` + "\n", want: 1, status: "429"},
		{name: "timeout", body: `{"type":"error","status":408,"error":"timeout","call_id":"c10"}` + "\n", want: 1, status: "timeout"},
		{name: "delayed usage", body: "{\"type\":\"assistant\",\"text\":\"done\",\"call_id\":\"c11\"}\n{\"type\":\"usage\",\"call_id\":\"c11\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}\n", want: 1, status: "ok", usage: true},
		{name: "resume", body: "{\"type\":\"resume\",\"session_id\":\"s2\",\"resume_of\":\"s1\",\"call_id\":\"c12\"}\n{\"type\":\"assistant\",\"text\":\"continued\",\"call_id\":\"c12\"}\n", want: 1, status: "ok", resume: "s1"},
		{name: "duplicate", body: "{\"type\":\"assistant\",\"text\":\"once\",\"call_id\":\"c13\"}\n{\"type\":\"assistant\",\"text\":\"again\",\"call_id\":\"c13\"}\n", want: 1, status: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MapClaudeStream(strings.NewReader(tc.body))
			if tc.malformed {
				if err == nil || !got.AccountedOnce() || len(got.Attempts) != tc.want {
					t.Fatalf("malformed err %v attempts %d", err, len(got.Attempts))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.AccountedOnce() || len(got.Attempts) != tc.want || got.Attempts[0].Status != tc.status {
				t.Fatalf("result %+v", got.Attempts)
			}
			if tc.usage && (got.Attempts[0].InputTokens != 3 || got.Attempts[0].OutputTokens != 4) {
				t.Fatalf("usage %+v", got.Attempts[0])
			}
			if got.ResumeOf != tc.resume {
				t.Fatalf("resume %q", got.ResumeOf)
			}
			if got.NegotiatedSteer {
				t.Fatal("stream set steer")
			}
		})
	}
}

func TestProveCredentialBoundary(t *testing.T) {
	const secret = "CANARY-PLANTED-SECRET-VALUE"
	parent := []string{
		"PATH=" + os.Getenv("PATH"),
		"PLANTED_SECRET=" + secret,
		"OPENAI_API_KEY=" + secret,
		"FOO=bar",
	}
	envOnly, err := ProveCredentialBoundary(context.Background(), BoundaryRequest{
		Evidence: EvidenceEnv,
		Secret:   secret,
		Parent:   parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if envOnly.Outcome != OutcomeLiveBlocked || envOnly.EnvOnlyIsolationAccepted || !envOnly.LiveBlockedIfUnproven {
		t.Fatalf("env-only %+v", envOnly)
	}
	got, err := ProveCredentialBoundary(context.Background(), BoundaryRequest{
		Evidence: EvidenceAdapter,
		Secret:   secret,
		Parent:   parent,
		Needles:  []string{"CANARY-OLD", "CANARY-NEW"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeIsolated || got.EvidenceClass != EvidenceAdapter || got.EnvOnlyIsolationAccepted {
		t.Fatalf("adapter %+v", got)
	}
	if strings.Contains(got.Bash, secret) || strings.Contains(got.MCP, secret) {
		t.Fatal("planted secret left the adapter")
	}
	if !strings.Contains(got.Bash, "FOO=bar") {
		t.Fatalf("bash dropped an ordinary variable: %q", got.Bash)
	}
	c := TaskConstraints(true)
	if c.DockerSocket || c.ForeignCheckoutWrite || c.BroadSecretMount {
		t.Fatalf("constraints %+v", c)
	}
}

func TestNativeToolsProductionAndContract(t *testing.T) {
	ctx := context.Background()
	scope := TokenScope{
		FactoryID:    "fac_1",
		TaskID:       "ntask_1",
		InvocationID: "inv_1",
		Role:         RoleLead,
		Grants: []string{
			GrantInboxRead, GrantAckWrite, GrantTaskRead, GrantTaskWrite,
			GrantCommentWrite, GrantRosterDispatch,
		},
		BudgetCents: 50,
	}
	lead, err := IssueRunToken(ctx, scope, Attenuation{})
	if err != nil {
		t.Fatal(err)
	}
	const planted = "CANARY-OLD"
	api := &fakeNative{taskID: "ntask_1", messageID: "nmsg_" + planted}
	tools := NativeTools{API: api, Needles: []string{planted, "CANARY-NEW"}}
	created, err := tools.Dispatch(ctx, lead, "native_task_create", map[string]string{"invocationId": "inv_1", "assignee": "lead"})
	if err != nil || created.Evidence != EvidenceProduction || created.Payload != "ntask_1" {
		t.Fatalf("create %+v %v", created, err)
	}
	for _, name := range []string{"native_task_get", "native_comment", "native_assign", "native_inbox_read", "native_apply", "native_ack", "native_redeliver", "native_claim", "native_wake"} {
		args := map[string]string{"taskId": "ntask_1", "assignee": "lead", "body": planted, "messageId": "nmsg_1", "deliveryId": "ndlv_1", "holder": "lead"}
		got, err := tools.Dispatch(ctx, lead, name, args)
		if err != nil || got.Evidence != EvidenceProduction {
			t.Fatalf("%s %+v %v", name, got, err)
		}
		if strings.Contains(got.Payload, planted) {
			t.Fatalf("%s leaked %s", name, got.Payload)
		}
	}
	if api.creates != 1 || api.gets != 1 || api.comments != 1 || api.inbox != 1 || api.acks != 1 {
		t.Fatalf("api calls %+v", api)
	}
	delegated, err := tools.Dispatch(ctx, lead, "native_delegate", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"})
	if err != nil || delegated.Evidence != EvidenceContract {
		t.Fatalf("delegate %+v %v", delegated, err)
	}
	waited, err := tools.Dispatch(ctx, lead, "native_wait", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"})
	if err != nil || waited.Evidence != EvidenceContract {
		t.Fatalf("wait %+v %v", waited, err)
	}
	spec, err := IssueRunToken(ctx, lead.Scope(), Attenuation{Role: RoleSpecialist, Grants: []string{GrantInboxRead, GrantRosterDispatch}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Dispatch(ctx, spec, "native_delegate", map[string]string{"taskId": "ntask_1", "invocationId": "inv_1"}); err == nil {
		t.Fatal("specialist delegated")
	}
	if _, err := tools.Dispatch(ctx, lead, "native_task_get", map[string]string{"taskId": "ntask_other"}); err == nil {
		t.Fatal("cross-task get succeeded")
	}
	if _, err := tools.Dispatch(ctx, lead, "native_task_create", map[string]string{"invocationId": "inv_other"}); err == nil {
		t.Fatal("cross-invocation create succeeded")
	}
}

func TestClaudeSignalAccountsOnce(t *testing.T) {
	bin := claudeFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	got, err := RunClaudeOnce(ctx, bin, "hold")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Status != "signal" || !got.AccountedOnce() {
		t.Fatalf("signal %+v", got.Attempts)
	}
}

func TestClaudeRunUsesPinnedFixture(t *testing.T) {
	bin := claudeFake(t)
	dir := t.TempDir()
	var stderr bytes.Buffer
	code := Run(context.Background(), Options{
		Args: []string{"agent", "run", "--task-id", "run_claude", "--harness", "claude-code"},
		Env: []string{
			"FACTORY_CLAUDE_BIN=" + bin,
			"FACTORY_CLAUDE_SCENARIO=empty-text",
			"FACTORY_SECRET_VALUES=CANARY-OLD",
			"PATH=" + os.Getenv("PATH"),
		},
		RuntimeDir: dir,
		Workspace:  dir,
		Stderr:     &stderr,
	})
	if code != ExitOK {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CANARY-OLD") || strings.Contains(string(raw), `"steer":true`) {
		t.Fatalf("events %s", raw)
	}
	if !strings.Contains(stderr.String(), "claude ok") {
		t.Fatalf("stderr %s", stderr.String())
	}
	helpOut, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if string(helpOut) != PinnedClaudeHelp() && !strings.Contains(string(helpOut), strings.TrimSpace(PinnedClaudeHelp())) {
		t.Fatalf("fake help drifted: %s", helpOut)
	}
}

type fakeNative struct {
	taskID    string
	messageID string
	creates   int
	gets      int
	comments  int
	inbox     int
	acks      int
}

func (f *fakeNative) CreateNativeTask(context.Context, string, client.NativeTaskRequest) (client.NativeTask, error) {
	f.creates++
	return client.NativeTask{ID: f.taskID}, nil
}

func (f *fakeNative) GetNativeTask(context.Context, string, string) (client.NativeTask, error) {
	f.gets++
	return client.NativeTask{ID: f.taskID}, nil
}

func (f *fakeNative) CommentNativeTask(context.Context, string, string, client.NativeCommentRequest) (client.NativeEventResult, error) {
	f.comments++
	return client.NativeEventResult{MessageID: f.messageID}, nil
}

func (f *fakeNative) AssignNativeTask(context.Context, string, string, string) (client.NativeEventResult, error) {
	return client.NativeEventResult{MessageID: "nmsg_1"}, nil
}

func (f *fakeNative) ReadNativeInbox(context.Context, string, string, string, int, time.Duration) (client.NativeInboxPage, error) {
	f.inbox++
	return client.NativeInboxPage{Deliveries: []client.NativeDelivery{{DeliveryID: "ndlv_1", InvocationID: "inv_1"}, {DeliveryID: "ndlv_other", InvocationID: "inv_other"}}}, nil
}

func (f *fakeNative) ApplyNativeMessage(context.Context, string, string, string) (client.NativeApplyResult, error) {
	return client.NativeApplyResult{MessageID: "nmsg_1"}, nil
}

func (f *fakeNative) AckNativeDelivery(context.Context, string, string, string) (client.NativeAckResult, error) {
	f.acks++
	return client.NativeAckResult{DeliveryID: "ndlv_1"}, nil
}

func (f *fakeNative) RedeliverNativeMessage(context.Context, string, string, string) (client.NativeRedelivery, error) {
	return client.NativeRedelivery{DeliveryID: "ndlv_2"}, nil
}

func (f *fakeNative) ClaimNativeInbox(context.Context, string, string, string) (client.NativeClaim, error) {
	return client.NativeClaim{Holder: "lead"}, nil
}

func (f *fakeNative) GetNativeWake(context.Context, string, string) (client.NativeWake, error) {
	return client.NativeWake{TaskID: "ntask_1"}, nil
}

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

var (
	fakeOnce sync.Once
	fakeBin  string
	fakeErr  error
)

func claudeFake(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "claude-fake")
		if err != nil {
			fakeErr = err
			return
		}
		fakeBin = filepath.Join(dir, "claude")
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			fakeErr = os.ErrNotExist
			return
		}
		cmd := exec.Command("go", "build", "-o", fakeBin, "./spike/lib/fakes/claude")
		cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fakeErr = fmt.Errorf("%w: %s", err, out)
		}
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeBin
}
