package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
)

func TestAgentRunIdleResyncTerminal(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	ns := newNamespace(t, ctx)
	var events, runs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/run":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "run-idle", "status": "queued"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			events.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := fmt.Fprint(w, "id: 1\nevent: run.status\ndata: {\"status\":\"running\"}\n\n"); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/agent/runs/"):
			runs.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             "run-idle",
				"status":         "failed",
				"failure_reason": "no_work_product",
				"failure":        map[string]any{"summary": "no pull request"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api := &fclient.Client{
		BaseURL:    srv.URL,
		APIKey:     "test-api-key",
		Backoff:    func(int) time.Duration { return 0 },
		StreamIdle: 200 * time.Millisecond,
	}
	reconciler := &AgentRunReconciler{Client: k8sClient, API: api, Base: ctx}
	createRepoFactory(t, ctx, ns, "acme-payments")
	run := &factoryv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "idle"},
		Spec: factoryv1.AgentRunSpec{
			FactoryRef: &factoryv1.LocalRef{Name: "acme-payments"},
			Prompt:     "ship it",
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got factoryv1.AgentRun
	for {
		if err := k8sClient.Get(ctx, key, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.Phase == "failed" && conditionTrue(got.Status.Conditions, condFailed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase %q conditions %v events %d runs %d", got.Status.Phase, got.Status.Conditions, events.Load(), runs.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if events.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("events %d runs %d", events.Load(), runs.Load())
	}
	reason := ""
	for _, cond := range got.Status.Conditions {
		if cond.Type == condFailed {
			reason = cond.Reason
		}
	}
	if reason != reasonNoWorkProduct {
		t.Fatalf("failed reason %q", reason)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if events.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("resync repeated: events %d runs %d", events.Load(), runs.Load())
	}
}
