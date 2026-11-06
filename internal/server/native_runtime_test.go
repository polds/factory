package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestNativeStageOwnerAdvance(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	ctx := context.Background()
	fac, err := env.srv.factory(ctx, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	item, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID, Source: store.SourceManual, Title: "native stage", Stage: store.StagePlanning,
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.srv.store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID: fac.ID, ProfileName: "scan", SourceOccurrenceKey: "native:stage:1",
		CanonicalDigest: "digest-stage", DefinitionDigest: "def", State: invocation.StateAccepted,
		Intent: "new", Priority: "interactive", WorkItemID: item.ID, NeutralStage: true,
		PrincipalJSON: json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !row.Created {
		t.Fatalf("invocation %+v %v", row, err)
	}
	agents, err := env.srv.store.ListAgents(ctx, fac.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents %+v %v", agents, err)
	}
	_, root, err := env.srv.store.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: fac.ID, InvocationID: row.Row.ID, AgentID: agents[0].ID,
		WorkItemID: item.ID, Stage: store.StagePlanning, Tree: serverProofTree(),
		CeilingCents: 2500, Slots: 2, MaxLive: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: row.Row.ID, ParentTaskID: root.ID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, env.srv.store, lead.Child.ID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := coordination.ClaimActivation(ctx, env.srv.store, root.ID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Complete(ctx, env.srv.store, lead.Child.ID, false); err != nil {
		t.Fatal(err)
	}
	env.srv.advanceAfterTerminal(ctx, child.RunID)
	got, err := env.srv.store.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.WorkItemOpen || got.Stage != store.StagePlanning {
		t.Fatalf("child advanced %+v", got)
	}
	if _, err := coordination.Complete(ctx, env.srv.store, root.ID, false); err != nil {
		t.Fatal(err)
	}
	env.srv.advanceAfterTerminal(ctx, owner.RunID)
	got, err = env.srv.store.GetWorkItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.WorkItemComplete {
		t.Fatalf("owner left %+v", got)
	}
}

func TestNativeRuntimeHTTP(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": "native:runtime:1",
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	ctx := context.Background()
	opened, err := c.OpenNativeTree(ctx, "pipe", invocationID, clientProofRequest())
	if err != nil || opened.StageOwner != "coordinator" || opened.RootTaskID == "" {
		t.Fatalf("open %+v %v", opened, err)
	}
	if _, err := c.DelegateNative(ctx, "pipe", opened.RootTaskID, client.NativeDelegateRequest{
		Assignee: "specialist-a", IdempotencyKey: "nope",
	}); err == nil || !strings.Contains(err.Error(), "delegation_denied") {
		t.Fatalf("delegate %v", err)
	}
	act, err := c.ActivateNative(ctx, "pipe", opened.RootTaskID, client.NativeActivateRequest{})
	if err != nil || act.RunID == "" || !act.StageOwner || act.Fence != 1 {
		t.Fatalf("activate %+v %v", act, err)
	}
	views, err := env.srv.ListNativeTrees(ctx, "pipe")
	if err != nil || len(views) != 1 || views[0].StageOwner != "coordinator" {
		t.Fatalf("views %+v %v", views, err)
	}
}

func serverProofTree() definition.NativeTree {
	return definition.NativeTree{
		NativeHierarchy: true, Synthetic: true,
		MaxDepth: definition.NativeProofMaxDepth, AbsoluteMaxDepth: definition.NativeAbsoluteMaxDepth,
		Delegation: "direct_manager",
		Personas: []definition.NativePersona{
			{Name: "coordinator", Role: "manager"},
			{Name: "lead", ReportsTo: "coordinator", SpawnableBy: []string{"coordinator"}, Role: "manager"},
			{Name: "specialist-a", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
			{Name: "specialist-b", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
		},
	}
}

func clientProofRequest() client.NativeTreeRequest {
	tree := serverProofTree()
	req := client.NativeTreeRequest{
		MaxDepth: tree.MaxDepth, AbsoluteMaxDepth: tree.AbsoluteMaxDepth,
		Delegation: tree.Delegation, Synthetic: true, NativeHierarchy: true,
		CeilingCents: 2500, Slots: 2, MaxLive: 4,
	}
	for _, p := range tree.Personas {
		req.Personas = append(req.Personas, client.NativePersona{
			Name: p.Name, ReportsTo: p.ReportsTo, SpawnableBy: p.SpawnableBy, Role: p.Role,
		})
	}
	return req
}
