package operator

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
)

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
