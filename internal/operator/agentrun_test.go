package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
)

func TestAgentRunLifecycle(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	var starts, cancels, gets atomic.Int32
	httpSrv := httptest.NewServer(agentHTTP(&starts, &cancels, &gets, false))
	t.Cleanup(httpSrv.Close)
	api := &fclient.Client{BaseURL: httpSrv.URL, APIKey: "test-api-key", Backoff: func(int) time.Duration { return 0 }}
	rec := record.NewFakeRecorder(16)
	reconciler := &AgentRunReconciler{Client: k8sClient, API: api, Recorder: rec, Base: ctx}
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "pay"},
		Spec: factoryv1.AgentRunSpec{
			FactoryRef: &factoryv1.LocalRef{Name: "acme-payments"},
			AgentRef:   &factoryv1.LocalRef{Name: "foreman"},
			Prompt:     "ship the fix",
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	restarted := &AgentRunReconciler{Client: k8sClient, API: api, Base: ctx}
	if _, err := restarted.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("starts %d", starts.Load())
	}
	deadline := time.Now().Add(10 * time.Second)
	var got factoryv1.AgentRun
	for {
		if err := k8sClient.Get(ctx, key, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.RunID == "run-1" && got.Status.Phase == "succeeded" && len(got.Status.Executions) == 1 && len(got.Status.PRURLs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase %q run %q executions %d", got.Status.Phase, got.Status.RunID, len(got.Status.Executions))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gets.Load() < 2 {
		t.Fatalf("event stream gets %d", gets.Load())
	}
	if err := k8sClient.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 {
		t.Fatalf("cancels %d", cancels.Load())
	}
	if err := k8sClient.Get(ctx, key, &got); err == nil {
		t.Fatal("finalizer was not removed")
	}
}

func TestAgentRunCancelGrace(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	var starts, cancels, gets atomic.Int32
	httpSrv := httptest.NewServer(agentHTTP(&starts, &cancels, &gets, true))
	t.Cleanup(httpSrv.Close)
	api := &fclient.Client{BaseURL: httpSrv.URL, APIKey: "test-api-key", Backoff: func(int) time.Duration { return 0 }}
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	reconciler := &AgentRunReconciler{Client: k8sClient, API: api, Base: ctx, Clock: clock}
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "stuck"},
		Spec:       factoryv1.AgentRunSpec{Prompt: "wait"},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 {
		t.Fatalf("cancels %d", cancels.Load())
	}
	clock.Advance(121 * time.Second)
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got factoryv1.AgentRun
	if err := k8sClient.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(got.Status.Conditions, condCancelTimedOut) {
		t.Fatal("CancelTimedOut is not set")
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, key, &got); err == nil {
		t.Fatal("finalizer was not removed after the grace")
	}
}

func agentHTTP(starts, cancels, gets *atomic.Int32, hold bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/run", func(w http.ResponseWriter, r *http.Request) {
		starts.Add(1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "run-1", "status": "queued"})
	})
	mux.HandleFunc("POST /api/v1/agent/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		cancels.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "status": "cancelling"})
	})
	mux.HandleFunc("GET /api/v1/agent/runs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		n := gets.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if hold {
			fmt.Fprint(w, "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		if n == 1 {
			fmt.Fprint(w, "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n")
			return
		}
		fmt.Fprint(w, "id: 2\nevent: run.terminal\ndata: {\"status\":\"succeeded\",\"executions\":[{\"id\":\"e1\",\"attempt\":1,\"state\":\"succeeded\"}],\"usage\":{\"inputTokens\":2,\"outputTokens\":3,\"costCents\":4},\"prUrls\":[\"https://example.invalid/pr/1\"]}\n\n")
	})
	return mux
}
