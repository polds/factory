package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	syaml "sigs.k8s.io/yaml"
)

func TestEnvtestCRDsAndCEL(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; envtest assets are not installed")
	}
	if _, err := os.Stat(filepath.Join(assets, "kube-apiserver")); err != nil {
		t.Skip("envtest assets are absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	crdDir := filepath.Join("..", "..", "..", "deploy", "helm", "factory", "crds")
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{crdDir},
		BinaryAssetsDirectory: assets,
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	sch := runtime.NewScheme()
	if err := scheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}
	applyExample(t, ctx, cl)
	testModelHarnessCEL(t, ctx, cl)
	testSignatureCEL(t, ctx, cl)
	testReviewerEmailsCEL(t, ctx, cl)
	testScorerCEL(t, ctx, cl)
	testAgentRunImmutable(t, ctx, cl)
}

func applyExample(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "crds", "example-resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range splitDocs(body) {
		obj, err := exampleObject(doc)
		if err != nil {
			t.Fatal(err)
		}
		if obj == nil {
			continue
		}
		if err := cl.Create(ctx, obj); err != nil {
			t.Fatalf("create %T: %v", obj, err)
		}
	}
}

func exampleObject(doc []byte) (client.Object, error) {
	var meta metav1.TypeMeta
	if err := syaml.Unmarshal(doc, &meta); err != nil {
		return nil, err
	}
	var obj client.Object
	switch meta.Kind {
	case "Namespace":
		obj = &corev1.Namespace{}
	case "Factory":
		obj = &Factory{}
	case "FactoryAgent":
		obj = &FactoryAgent{}
	case "Runner":
		obj = &Runner{}
	case "Automation":
		obj = &Automation{}
	case "Scorer":
		obj = &Scorer{}
	case "WorkerPool":
		obj = &WorkerPool{}
	case "AgentRun":
		obj = &AgentRun{}
	default:
		return nil, nil
	}
	if err := syaml.Unmarshal(doc, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func testModelHarnessCEL(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	ns := "cel-model"
	mustCreate(t, ctx, cl, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	mustCreate(t, ctx, cl, &FactoryAgent{ObjectMeta: metav1.ObjectMeta{Name: "model-only", Namespace: ns}, Spec: FactoryAgentSpec{Model: "auto"}})
	mustCreate(t, ctx, cl, &FactoryAgent{ObjectMeta: metav1.ObjectMeta{Name: "harness-only", Namespace: ns}, Spec: FactoryAgentSpec{Harness: &HarnessSpec{Type: "claude-code", Model: "auto"}}})
	mustReject(t, ctx, cl, &FactoryAgent{ObjectMeta: metav1.ObjectMeta{Name: "both", Namespace: ns}, Spec: FactoryAgentSpec{Model: "auto", Harness: &HarnessSpec{Type: "claude-code"}}})
	mustReject(t, ctx, cl, &FactoryAgent{ObjectMeta: metav1.ObjectMeta{Name: "neither", Namespace: ns}, Spec: FactoryAgentSpec{Description: "no model"}})
}

func testSignatureCEL(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	ns := "cel-webhook"
	mustCreate(t, ctx, cl, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	mustCreate(t, ctx, cl, &FactoryWebhook{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: ns}, Spec: FactoryWebhookSpec{AuthMode: "token", SecretName: "CI_WEBHOOK"}})
	mustCreate(t, ctx, cl, &FactoryWebhook{ObjectMeta: metav1.ObjectMeta{Name: "signed", Namespace: ns}, Spec: FactoryWebhookSpec{AuthMode: "signature", SignatureScheme: "github", SecretName: "CI_WEBHOOK"}})
	mustReject(t, ctx, cl, &FactoryWebhook{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: ns}, Spec: FactoryWebhookSpec{AuthMode: "signature", SecretName: "CI_WEBHOOK"}})
	mustReject(t, ctx, cl, &FactoryWebhook{ObjectMeta: metav1.ObjectMeta{Name: "extra", Namespace: ns}, Spec: FactoryWebhookSpec{AuthMode: "token", SignatureScheme: "github", SecretName: "CI_WEBHOOK"}})
}

func testReviewerEmailsCEL(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	ns := "cel-review"
	mustCreate(t, ctx, cl, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	mustCreate(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "custom", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "custom", ReviewerEmails: []string{"a@acme.example"}}}})
	mustReject(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "custom-empty", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "custom"}}})
	mustReject(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "custom-blank", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "custom", ReviewerEmails: []string{" "}}}})
	mustCreate(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "custom-mixed", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "custom", ReviewerEmails: []string{" ", "a@acme.example"}}}})
	mustReject(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "admins-mail", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "admins", ReviewerEmails: []string{"a@acme.example"}}}})
	mustCreate(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "admins-blank", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "admins", ReviewerEmails: []string{" "}}}})
	mustCreate(t, ctx, cl, &Factory{ObjectMeta: metav1.ObjectMeta{Name: "admins", Namespace: ns}, Spec: FactorySpec{SelfImprovement: &SelfImprovementSpec{ReviewerType: "admins"}}})
}

func testScorerCEL(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	ns := "cel-scorer"
	mustCreate(t, ctx, cl, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	mustCreate(t, ctx, cl, &Scorer{ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: ns}, Spec: ScorerSpec{
		PassingScore: 1,
		Labels: []ScorerLabelSpec{
			{Value: "pass", Score: 1},
			{Value: "fail", Score: 0},
		},
	}})
	mustReject(t, ctx, cl, &Scorer{ObjectMeta: metav1.ObjectMeta{Name: "all-pass", Namespace: ns}, Spec: ScorerSpec{
		PassingScore: 1,
		Labels:       []ScorerLabelSpec{{Value: "pass", Score: 1}, {Value: "also", Score: 2}},
	}})
	mustReject(t, ctx, cl, &Scorer{ObjectMeta: metav1.ObjectMeta{Name: "all-fail", Namespace: ns}, Spec: ScorerSpec{
		PassingScore: 1,
		Labels:       []ScorerLabelSpec{{Value: "fail", Score: 0}, {Value: "worse", Score: 0}},
	}})
}

func testAgentRunImmutable(t *testing.T, ctx context.Context, cl client.Client) {
	t.Helper()
	ns := "cel-run"
	mustCreate(t, ctx, cl, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	run := &AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "hotfix", Namespace: ns}, Spec: AgentRunSpec{Prompt: "fix it", Priority: "interactive"}}
	mustCreate(t, ctx, cl, run)
	original := run.DeepCopy()
	run.Spec.Prompt = "changed"
	if err := cl.Update(ctx, run); err == nil {
		t.Fatal("spec update was accepted")
	} else if !apierrors.IsInvalid(err) {
		t.Fatalf("spec update: %v", err)
	}
	fresh := original.DeepCopy()
	if err := cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: "hotfix"}, fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Labels = map[string]string{"keep": "yes"}
	if err := cl.Update(ctx, fresh); err != nil {
		t.Fatalf("label update: %v", err)
	}
}

func mustCreate(t *testing.T, ctx context.Context, cl client.Client, obj client.Object) {
	t.Helper()
	if err := cl.Create(ctx, obj); err != nil {
		t.Fatalf("create %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
	}
}

func mustReject(t *testing.T, ctx context.Context, cl client.Client, obj client.Object) {
	t.Helper()
	err := cl.Create(ctx, obj)
	if err == nil {
		t.Fatalf("create %s/%s was accepted", obj.GetNamespace(), obj.GetName())
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("create %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
	}
}
