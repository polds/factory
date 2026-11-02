package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

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

func TestParseRunEventCopiesPRURLs(t *testing.T) {
	got := parseRunEvent(fclient.Event{
		Type: "run.terminal",
		Data: `{"status":"succeeded","prUrls":["https://pr.invalid/m7/factory-m7-target/pull/1"]}`,
	})
	if got.phase != "succeeded" || len(got.prs) != 1 || got.prs[0] != "https://pr.invalid/m7/factory-m7-target/pull/1" {
		t.Fatalf("parsed %+v", got)
	}
	snake := parseRunEvent(fclient.Event{Type: "run.terminal", Data: `{"pr_urls":["https://example.invalid/pull/2"]}`})
	if len(snake.prs) != 1 || snake.prs[0] != "https://example.invalid/pull/2" {
		t.Fatalf("snake %+v", snake.prs)
	}
}

func TestAgentRunPredicateRequeuesRunIDAndTerminalPhase(t *testing.T) {
	pred := agentRunPredicate()
	base := &factoryv1.AgentRun{}
	base.Generation = 2
	withRun := base.DeepCopy()
	withRun.Status.RunID = "run-1"
	if !pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: withRun}) {
		t.Fatal("run id did not requeue")
	}
	running := withRun.DeepCopy()
	running.Status.Phase = "running"
	if pred.Update(event.UpdateEvent{ObjectOld: withRun, ObjectNew: running}) {
		t.Fatal("non-terminal phase requeued")
	}
	done := running.DeepCopy()
	done.Status.Phase = "cancelled"
	if !pred.Update(event.UpdateEvent{ObjectOld: running, ObjectNew: done}) {
		t.Fatal("terminal phase did not requeue")
	}
	if pred.Update(event.UpdateEvent{ObjectOld: done, ObjectNew: done.DeepCopy()}) {
		t.Fatal("unchanged terminal phase requeued")
	}
}

func TestAgentRunTerminalPhaseDropsFinalizer(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	var once sync.Once
	release := make(chan struct{})
	signal := func() { once.Do(func() { close(release) }) }
	var starts atomic.Int32
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/run":
			starts.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "run-hold", "status": "queued"})
		default:
			if strings.HasSuffix(r.URL.Path, "/cancel") {
				signal()
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "run-hold", "status": "cancelling"})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := fmt.Fprint(w, "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n"); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
			_, _ = fmt.Fprint(w, "id: 2\nevent: run.terminal\ndata: {\"status\":\"cancelled\",\"prUrls\":[\"https://pr.invalid/m7/factory-m7-target/pull/1\"]}\n\n")
		}
	}))
	t.Cleanup(httpSrv.Close)
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 Scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	api := &fclient.Client{BaseURL: httpSrv.URL, APIKey: "test-api-key", Backoff: func(int) time.Duration { return 0 }}
	reconciler := &AgentRunReconciler{Client: mgr.GetClient(), API: api, Recorder: record.NewFakeRecorder(8), Base: ctx}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "hold"},
		Spec:       factoryv1.AgentRunSpec{Prompt: "wait"},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	var got factoryv1.AgentRun
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := k8sClient.Get(ctx, key, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.RunID == "run-hold" && got.Status.Phase == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not start phase %q run %q", got.Status.Phase, got.Status.RunID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if starts.Load() != 1 {
		t.Fatalf("starts %d", starts.Load())
	}
	started := time.Now()
	if err := k8sClient.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		err := k8sClient.Get(ctx, key, &got)
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("finalizer still held after %s phase %q", time.Since(started), got.Status.Phase)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("delete-to-cancel %s", elapsed)
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
			if _, err := fmt.Fprint(w, "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n"); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		body := "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n"
		if n != 1 {
			body = "id: 2\nevent: run.terminal\ndata: {\"status\":\"succeeded\",\"executions\":[{\"id\":\"e1\",\"attempt\":1,\"state\":\"succeeded\"}],\"usage\":{\"inputTokens\":2,\"outputTokens\":3,\"costCents\":4},\"prUrls\":[\"https://example.invalid/pr/1\"]}\n\n"
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			return
		}
	})
	return mux
}
