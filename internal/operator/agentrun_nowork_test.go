package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
)

func TestParseRunEventIgnoresHarnessTerminal(t *testing.T) {
	succeeded := parseRunEvent(fclient.Event{Type: "terminal", Data: `{"status":"succeeded","summary":"pull request opened"}`})
	if succeeded.phase != "" || succeeded.summary != "" {
		t.Fatalf("harness succeeded %+v", succeeded)
	}
	failed := parseRunEvent(fclient.Event{Type: "terminal", Data: `{"status":"failed","failure_reason":"failed_by_agent"}`})
	if failed.phase != "" || failed.reason != "" {
		t.Fatalf("harness failed %+v", failed)
	}
	withPR := parseRunEvent(fclient.Event{Type: "tool.result", Data: `{"prUrls":["https://example.invalid/pr/1"]}`})
	if withPR.phase != "" || len(withPR.prs) != 1 {
		t.Fatalf("tool.result %+v", withPR)
	}
	running := parseRunEvent(fclient.Event{Type: "run.status", Data: `{"status":"running"}`})
	if running.phase != "running" {
		t.Fatalf("run.status %+v", running)
	}
	decided := parseRunEvent(fclient.Event{Type: "run.terminal", Data: `{"status":"succeeded"}`})
	if decided.phase != "succeeded" {
		t.Fatalf("run.terminal %+v", decided)
	}
}

func TestParseRunEventReadsNoWorkProduct(t *testing.T) {
	got := parseRunEvent(fclient.Event{
		Type: "run.terminal",
		Data: `{"status":"failed","failure_reason":" no_work_product ","summary":" agent finished without opening a pull request "}`,
	})
	if got.phase != "failed" || got.reason != "no_work_product" || got.summary != "agent finished without opening a pull request" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestFailedConditionRedactsNoWorkProduct(t *testing.T) {
	const token = "sk-plantedsecrettokenvalue"
	run := &factoryv1.AgentRun{}
	run.Generation = 3
	cond := failedCondition(run, "no_work_product", "agent finished without opening a pull request Bearer "+token, time.Unix(10, 0).UTC())
	if cond.Type != condFailed || cond.Status != metav1.ConditionTrue || cond.Reason != reasonNoWorkProduct {
		t.Fatalf("condition %+v", cond)
	}
	if cond.ObservedGeneration != 3 {
		t.Fatalf("generation %d", cond.ObservedGeneration)
	}
	if strings.Contains(cond.Message, token) {
		t.Fatal("condition message kept the token")
	}
	if !strings.Contains(cond.Message, "no_work_product: agent finished without opening a pull request") {
		t.Fatalf("message %q", cond.Message)
	}
	if !strings.Contains(cond.Message, "Bearer [REDACTED]") {
		t.Fatalf("bearer was not redacted: %q", cond.Message)
	}
	other := failedConditionReason("image_pull")
	if other != condFailed {
		t.Fatalf("reason %s", other)
	}
}

func TestAgentRunWatchAppliesFactoryTerminalAfterHarness(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	const body = "" +
		"id: 1\nevent: tool.result\ndata: {}\n\n" +
		"id: 2\nevent: terminal\ndata: {\"status\":\"succeeded\",\"summary\":\"pull request opened\"}\n\n" +
		"id: 3\nevent: run.terminal\ndata: {\"status\":\"failed\",\"failure_reason\":\"no_work_product\",\"summary\":\"agent finished without opening a pull request\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	watchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(cancel)
	reconciler := &AgentRunReconciler{
		Client: k8sClient,
		Base:   watchCtx,
		API: &fclient.Client{
			BaseURL: srv.URL,
			APIKey:  "test-key",
			Backoff: func(int) time.Duration { return 0 },
		},
	}
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "harness-then-factory"},
		Spec:       factoryv1.AgentRunSpec{Prompt: "open a pull request"},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	reconciler.watch(watchCtx, key, "run-1")
	var got factoryv1.AgentRun
	if err := k8sClient.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "failed" {
		t.Fatalf("phase %q", got.Status.Phase)
	}
	var cond *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == condFailed {
			cond = &got.Status.Conditions[i]
			break
		}
	}
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonNoWorkProduct {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
}

func TestAgentRunRecordsNoWorkProduct(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	reconciler := &AgentRunReconciler{Client: k8sClient, Base: ctx}
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "missing-pr"},
		Spec: factoryv1.AgentRunSpec{
			Prompt: "open a pull request",
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	err := reconciler.applyEvent(ctx, key, fclient.Event{
		Type: "run.terminal",
		Data: `{"status":"failed","failure_reason":"no_work_product","summary":"agent finished without opening a pull request Bearer sk-plantedsecrettokenvalue"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got factoryv1.AgentRun
	if err := k8sClient.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "failed" {
		t.Fatalf("phase %q", got.Status.Phase)
	}
	var cond *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == condFailed {
			cond = &got.Status.Conditions[i]
			break
		}
	}
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonNoWorkProduct {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
	if strings.Contains(cond.Message, "sk-plantedsecrettokenvalue") {
		t.Fatal("status message kept the token")
	}
	if !strings.Contains(cond.Message, "no_work_product: agent finished without opening a pull request") {
		t.Fatalf("message %q", cond.Message)
	}
}
