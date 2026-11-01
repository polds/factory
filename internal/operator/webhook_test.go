package operator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
)

func TestWebhookRules(t *testing.T) {
	ctx := context.Background()
	ns := "admit"
	const planted = "sk-plantedWebhookSecret99"

	runner := &factoryv1.Runner{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "local"}}
	foreman := &factoryv1.FactoryAgent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "foreman"}, Spec: factoryv1.FactoryAgentSpec{
		FactoryRef: &factoryv1.LocalRef{Name: "acme"},
		AgentType:  "FOREMAN",
		Model:      "auto",
	}}
	v := &Validator{Client: fake.NewClientBuilder().WithScheme(Scheme).WithObjects(runner, foreman).Build()}

	t.Run("runner exists", func(t *testing.T) {
		if err := v.admit(ctx, nil, factoryWithRunner(ns, "local")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("runner missing", func(t *testing.T) {
		err := v.admit(ctx, nil, factoryWithRunner(ns, planted))
		if err == nil {
			t.Fatal("missing runner was admitted")
		}
		if strings.Contains(err.Error(), planted) {
			t.Fatal("deny leaked a secret-shaped runner name")
		}
		if !strings.Contains(err.Error(), "runner") {
			t.Fatal(err)
		}
	})

	t.Run("automation agent exists", func(t *testing.T) {
		if err := v.admit(ctx, nil, automation(ns, "foreman")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("automation agent missing", func(t *testing.T) {
		err := v.admit(ctx, nil, automation(ns, "missing"))
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("one foreman", func(t *testing.T) {
		next := foreman.DeepCopy()
		next.Spec.Description = "still the foreman"
		if err := v.admit(ctx, foreman, next); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("second foreman", func(t *testing.T) {
		extra := foreman.DeepCopy()
		extra.Name = "foreman-2"
		err := v.admit(ctx, nil, extra)
		if err == nil || !strings.Contains(err.Error(), "exactly one FOREMAN") {
			t.Fatalf("err %v", err)
		}
	})

	base := factoryWithApprovals(ns, true)
	t.Run("approvals hold", func(t *testing.T) {
		if err := v.admit(ctx, base, base.DeepCopy()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("approvals weakened", func(t *testing.T) {
		err := v.admit(ctx, base, factoryWithApprovals(ns, false))
		if err == nil {
			t.Fatal("weakened approvals were admitted")
		}
		if !strings.Contains(err.Error(), "pipeline.approvals:") || !strings.Contains(err.Error(), "approval gate") {
			t.Fatal(err)
		}
	})
}

func factoryWithRunner(ns, runner string) *factoryv1.Factory {
	return &factoryv1.Factory{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "acme"},
		Spec: factoryv1.FactorySpec{
			AgentDefaults: &factoryv1.AgentDefaultsSpec{Runner: runner, Model: "auto"},
		},
	}
}

func automation(ns, agent string) *factoryv1.Automation {
	return &factoryv1.Automation{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "nightly"},
		Spec: factoryv1.AutomationSpec{
			FactoryRef: &factoryv1.LocalRef{Name: "acme"},
			AgentRef:   &factoryv1.LocalRef{Name: agent},
		},
	}
}

func factoryWithApprovals(ns string, required bool) *factoryv1.Factory {
	body := "schemaVersion: v1alpha1\nname: acme\npipeline:\n  approvals:\n    ship:\n      required: true\n      approvers: role:editor\n"
	if !required {
		body = "schemaVersion: v1alpha1\nname: acme\npipeline:\n  approvals:\n    ship:\n      required: false\n      approvers: role:editor\n"
	}
	return &factoryv1.Factory{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "acme"},
		Spec: factoryv1.FactorySpec{
			Files: []factoryv1.SourceFile{{Path: "factory.yaml", Content: body}},
		},
	}
}
