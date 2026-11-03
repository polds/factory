package operator

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/api"
	fclient "github.com/polds/factory/internal/client"
)

// collideAPI copies factoryd's short-circuit: a generation that is already
// serving is answered Valid and is not stored again.
type collideAPI struct {
	mu       sync.Mutex
	serving  int64
	files    []api.DefinitionFile
	gens     []int64
	rewrites int
}

func (c *collideAPI) PushDefinition(_ context.Context, _ string, req fclient.DefinitionPushRequest) (fclient.DefinitionPushResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gens = append(c.gens, req.Generation)
	if c.serving > 0 && req.Generation <= c.serving {
		return fclient.DefinitionPushResponse{Valid: true, PublishedGeneration: c.serving}, nil
	}
	c.serving = req.Generation
	c.files = append([]api.DefinitionFile(nil), req.Files...)
	c.rewrites++
	return fclient.DefinitionPushResponse{Valid: true, PublishedGeneration: req.Generation}, nil
}

func (c *collideAPI) GetDefinition(context.Context, string) (api.DefinitionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return api.DefinitionView{Files: append([]api.DefinitionFile(nil), c.files...)}, nil
}

func (c *collideAPI) has(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, file := range c.files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func TestPublishCollision(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)

	t.Run("same generation different bytes", func(t *testing.T) {
		key, stub := publishFactory(t, ctx, 1, factoryYAML, "implementer", "hold")
		if _, err := newReconciler(stub).Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if !stub.has("agents/implementer/agent.md") || !stub.has("agents/hold/agent.md") {
			t.Fatal("burst was not applied")
		}
		assertServedClaim(t, ctx, key, stub)
	})

	t.Run("older generation", func(t *testing.T) {
		key, stub := publishFactory(t, ctx, 5, factoryYAML)
		var fac factoryv1.Factory
		if err := k8sClient.Get(ctx, key, &fac); err != nil {
			t.Fatal(err)
		}
		fac.Annotations = map[string]string{
			annGeneration: "1",
			annHash:       bundleHash([]api.DefinitionFile{{Path: "factory.yaml", Content: factoryYAML}}),
		}
		if err := k8sClient.Update(ctx, &fac); err != nil {
			t.Fatal(err)
		}
		if err := k8sClient.Create(ctx, testAgent(key.Namespace, "implementer")); err != nil {
			t.Fatal(err)
		}
		if _, err := newReconciler(stub).Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if !stub.has("agents/implementer/agent.md") {
			t.Fatal("already-served generation was left in place")
		}
		assertServedClaim(t, ctx, key, stub)
	})
}

func TestPublishIdenticalResend(t *testing.T) {
	requireEnvtest(t)
	ctx := testContext(t)
	key, stub := publishFactory(t, ctx, 0, factoryYAML)
	reconciler := newReconciler(stub)
	req := ctrl.Request{NamespacedName: key}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	rewrites := stub.rewrites
	var fac factoryv1.Factory
	if err := k8sClient.Get(ctx, key, &fac); err != nil {
		t.Fatal(err)
	}
	fac.Status = factoryv1.FactoryStatus{}
	if err := k8sClient.Status().Update(ctx, &fac); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	gens := append([]int64(nil), stub.gens...)
	rewritten := stub.rewrites
	stub.mu.Unlock()
	if rewritten != rewrites {
		t.Fatalf("identical resend rewrote the bundle (%d -> %d)", rewrites, rewritten)
	}
	if len(gens) != 2 || gens[0] != gens[1] {
		t.Fatalf("gens %v", gens)
	}
	assertServedClaim(t, ctx, key, stub)
}

const factoryYAML = "name: m7\n"

func publishFactory(t *testing.T, ctx context.Context, serving int64, primed string, agents ...string) (client.ObjectKey, *collideAPI) {
	t.Helper()
	ns := newNamespace(t, ctx)
	fac := &factoryv1.Factory{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "m7"},
		Spec: factoryv1.FactorySpec{Files: []factoryv1.SourceFile{{
			Path:    "factory.yaml",
			Content: factoryYAML,
		}}},
	}
	if err := k8sClient.Create(ctx, fac); err != nil {
		t.Fatal(err)
	}
	for _, name := range agents {
		if err := k8sClient.Create(ctx, testAgent(ns, name)); err != nil {
			t.Fatal(err)
		}
	}
	stub := &collideAPI{}
	if serving > 0 {
		stub.serving = serving
		stub.files = []api.DefinitionFile{{Path: "factory.yaml", Content: primed}}
	}
	return client.ObjectKey{Namespace: ns, Name: "m7"}, stub
}

func testAgent(ns, name string) *factoryv1.FactoryAgent {
	return &factoryv1.FactoryAgent{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: factoryv1.FactoryAgentSpec{
			FactoryRef: &factoryv1.LocalRef{Name: "m7"},
			AgentType:  "IMPLEMENT",
			Model:      "m7-agent",
			Runner:     "local",
			Files: []factoryv1.SourceFile{{
				Path:    "agents/" + name + "/agent.md",
				Content: name + "\n",
			}},
		},
	}
}

func newReconciler(api DefinitionAPI) *FactoryReconciler {
	return &FactoryReconciler{Client: k8sClient, API: api, Log: logr.Discard()}
}

func assertServedClaim(t *testing.T, ctx context.Context, key client.ObjectKey, api *collideAPI) {
	t.Helper()
	var fac factoryv1.Factory
	if err := k8sClient.Get(ctx, key, &fac); err != nil {
		t.Fatal(err)
	}
	assertValid(t, &fac, true)
	api.mu.Lock()
	defer api.mu.Unlock()
	if fac.Annotations[annHash] != bundleHash(api.files) {
		t.Fatalf("annotation claims a bundle factoryd is not serving (gens %v)", api.gens)
	}
	if fac.Annotations[annGeneration] != strconv.FormatInt(api.serving, 10) {
		t.Fatalf("annotation generation %s serving %d gens %v", fac.Annotations[annGeneration], api.serving, api.gens)
	}
}
