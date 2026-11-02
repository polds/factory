package workerpool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
)

func TestCreateScaleDeleteAndDrift(t *testing.T) {
	requireEnvtest(t)
	ctx, logs := captureLogs(t)
	poolNS := makeNS(t, ctx, "p")
	taskNS := makeNS(t, ctx, "t")
	cfg := testConfig(taskNS)
	rec := record.NewFakeRecorder(32)
	r := &Reconciler{Client: k8sClient, Recorder: rec, Config: cfg}
	pool := testPool(poolNS, taskNS, 2)
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: poolNS, Name: pool.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}

	stack, err := Build(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sa := &corev1.ServiceAccount{}
	cm := &corev1.ConfigMap{}
	dep := &appsv1.Deployment{}
	role := &rbacv1.Role{}
	binding := &rbacv1.RoleBinding{}
	mustGet(t, ctx, client.ObjectKeyFromObject(stack.ServiceAccount), sa)
	mustGet(t, ctx, client.ObjectKeyFromObject(stack.ConfigMap), cm)
	mustGet(t, ctx, client.ObjectKeyFromObject(stack.Deployment), dep)
	mustGet(t, ctx, client.ObjectKeyFromObject(stack.Role), role)
	mustGet(t, ctx, client.ObjectKeyFromObject(stack.RoleBinding), binding)
	if sa.Namespace != poolNS || cm.Namespace != poolNS || dep.Namespace != poolNS {
		t.Fatalf("same-namespace objects: sa %s cm %s deploy %s", sa.Namespace, cm.Namespace, dep.Namespace)
	}
	if role.Namespace != taskNS || binding.Namespace != taskNS {
		t.Fatalf("task namespace objects: role %s binding %s", role.Namespace, binding.Namespace)
	}
	assertOwner(t, pool, sa, cm, dep)
	wantLabel := PoolLabel(poolNS, pool.Name)
	if role.Labels[LabelPool] != wantLabel || binding.Labels[LabelPool] != wantLabel {
		t.Fatalf("task objects missing pool label %s", wantLabel)
	}
	checksum := dep.Spec.Template.Annotations["checksum/config"]
	if checksum == "" || cm.Data["config.yaml"] == "" {
		t.Fatal("config or checksum missing")
	}

	var fresh factoryv1.WorkerPool
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.Replicas = 5
	if err := k8sClient.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	mustGet(t, ctx, client.ObjectKeyFromObject(dep), dep)
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 5 {
		t.Fatalf("replicas = %v", dep.Spec.Replicas)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); err != nil {
		t.Fatal(err)
	}
	// WorkerPoolStatus has no top-level observedGeneration (the type is frozen).
	// The Ready condition records metadata.generation.
	cond := readyCondition(t, &fresh)
	if cond.ObservedGeneration != fresh.Generation {
		t.Fatalf("Ready.observedGeneration = %d, generation %d", cond.ObservedGeneration, fresh.Generation)
	}

	fresh.Spec.KubernetesBackend.DefaultImage = "ubuntu:24.04"
	if err := k8sClient.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	mustGet(t, ctx, client.ObjectKeyFromObject(cm), cm)
	mustGet(t, ctx, client.ObjectKeyFromObject(dep), dep)
	if !strings.Contains(cm.Data["config.yaml"], "ubuntu:24.04") {
		t.Fatal("configmap was not re-rendered")
	}
	if dep.Spec.Template.Annotations["checksum/config"] == checksum {
		t.Fatal("checksum/config did not roll")
	}

	edited := int32(9)
	dep.Spec.Replicas = &edited
	if err := k8sClient.Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	role.Rules = []rbacv1.PolicyRule{{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get"}}}
	if err := k8sClient.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	mustGet(t, ctx, client.ObjectKeyFromObject(dep), dep)
	mustGet(t, ctx, client.ObjectKeyFromObject(role), role)
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 5 {
		t.Fatalf("drifted replicas stayed %v", dep.Spec.Replicas)
	}
	if len(role.Rules) != 4 {
		t.Fatalf("drifted role rules stayed %d", len(role.Rules))
	}

	// envtest does not run a garbage collector. Same-namespace objects must
	// keep a controller owner reference; only the task-namespace Role and
	// RoleBinding are deleted by the finalizer.
	if err := k8sClient.Delete(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); !apierrors.IsNotFound(err) {
		t.Fatalf("pool release: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(role), role); !apierrors.IsNotFound(err) {
		t.Fatalf("role still present: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding); !apierrors.IsNotFound(err) {
		t.Fatalf("rolebinding still present: %v", err)
	}
	mustGet(t, ctx, client.ObjectKeyFromObject(sa), sa)
	mustGet(t, ctx, client.ObjectKeyFromObject(cm), cm)
	mustGet(t, ctx, client.ObjectKeyFromObject(dep), dep)
	assertOwner(t, &factoryv1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Name: pool.Name, Namespace: poolNS, UID: pool.UID}}, sa, cm, dep)
	if strings.Contains(logs.String(), "sk-") || strings.Contains(eventText(rec), "sk-") {
		t.Fatal("reconcile log or event looked like a token")
	}
}

func TestMoveTaskNamespaceThenDelete(t *testing.T) {
	requireEnvtest(t)
	ctx, _ := captureLogs(t)
	poolNS := makeNS(t, ctx, "p")
	nsA := makeNS(t, ctx, "a")
	nsB := makeNS(t, ctx, "b")
	nsC := makeNS(t, ctx, "c")
	denied := makeNS(t, ctx, "d")

	t.Run("A to B", func(t *testing.T) {
		r := newMoveReconciler(nsA, nsB, nsC)
		req := reconcilePoolIn(t, ctx, r, poolNS, "move-ab", nsA)
		movePoolNamespace(t, ctx, r, req, nsB)
		assertLabeledRBAC(t, ctx, nsA, PoolLabel(poolNS, "move-ab"), 0)
		assertLabeledRBAC(t, ctx, nsB, PoolLabel(poolNS, "move-ab"), 1)
		deletePool(t, ctx, r, req)
		assertNoLabeledRBAC(t, ctx, PoolLabel(poolNS, "move-ab"))
	})

	t.Run("A to B to C", func(t *testing.T) {
		r := newMoveReconciler(nsA, nsB, nsC)
		req := reconcilePoolIn(t, ctx, r, poolNS, "move-abc", nsA)
		movePoolNamespace(t, ctx, r, req, nsB)
		movePoolNamespace(t, ctx, r, req, nsC)
		assertLabeledRBAC(t, ctx, nsA, PoolLabel(poolNS, "move-abc"), 0)
		assertLabeledRBAC(t, ctx, nsB, PoolLabel(poolNS, "move-abc"), 0)
		assertLabeledRBAC(t, ctx, nsC, PoolLabel(poolNS, "move-abc"), 1)
		deletePool(t, ctx, r, req)
		assertNoLabeledRBAC(t, ctx, PoolLabel(poolNS, "move-abc"))
	})

	t.Run("A to disallowed", func(t *testing.T) {
		cfg := fixtureConfig()
		cfg.TaskNamespaces = []string{nsA}
		cfg.ImagePullSecrets = nil
		r := &Reconciler{Client: k8sClient, Recorder: record.NewFakeRecorder(8), Config: cfg}
		req := reconcilePoolIn(t, ctx, r, poolNS, "move-deny", nsA)
		movePoolNamespace(t, ctx, r, req, denied)
		var got factoryv1.WorkerPool
		if err := k8sClient.Get(ctx, req.NamespacedName, &got); err != nil {
			t.Fatal(err)
		}
		cond := readyCondition(t, &got)
		if cond.Status != metav1.ConditionFalse || cond.Reason != "TaskNamespaceNotAllowed" {
			t.Fatalf("disallowed condition: %+v", cond)
		}
		assertLabeledRBAC(t, ctx, nsA, PoolLabel(poolNS, "move-deny"), 0)
		assertLabeledRBAC(t, ctx, denied, PoolLabel(poolNS, "move-deny"), 0)
		deletePool(t, ctx, r, req)
		assertNoLabeledRBAC(t, ctx, PoolLabel(poolNS, "move-deny"))
	})
}

func newMoveReconciler(namespaces ...string) *Reconciler {
	cfg := fixtureConfig()
	cfg.TaskNamespaces = namespaces
	cfg.ImagePullSecrets = nil
	return &Reconciler{Client: k8sClient, Recorder: record.NewFakeRecorder(8), Config: cfg}
}

func reconcilePoolIn(t *testing.T, ctx context.Context, r *Reconciler, poolNS, name, taskNS string) ctrl.Request {
	t.Helper()
	pool := testPool(poolNS, taskNS, 1)
	pool.Name = name
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: poolNS, Name: name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	assertLabeledRBAC(t, ctx, taskNS, PoolLabel(poolNS, name), 1)
	return req
}

func movePoolNamespace(t *testing.T, ctx context.Context, r *Reconciler, req ctrl.Request, taskNS string) {
	t.Helper()
	var fresh factoryv1.WorkerPool
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.KubernetesBackend.Namespace = taskNS
	if err := k8sClient.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
}

func deletePool(t *testing.T, ctx context.Context, r *Reconciler, req ctrl.Request) {
	t.Helper()
	var fresh factoryv1.WorkerPool
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Delete(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, &fresh); !apierrors.IsNotFound(err) {
		t.Fatalf("pool release: %v", err)
	}
}

func assertLabeledRBAC(t *testing.T, ctx context.Context, namespace, label string, want int) {
	t.Helper()
	roles, bindings := labeledRBACCounts(t, ctx, namespace, label)
	if roles != want || bindings != want {
		t.Fatalf("namespace %s labeled rbac roles %d bindings %d, want %d", namespace, roles, bindings, want)
	}
}

func assertNoLabeledRBAC(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	roles, bindings := labeledRBACCounts(t, ctx, "", label)
	if roles != 0 || bindings != 0 {
		t.Fatalf("labeled rbac remains roles %d bindings %d", roles, bindings)
	}
}

func labeledRBACCounts(t *testing.T, ctx context.Context, namespace, label string) (int, int) {
	t.Helper()
	var roles rbacv1.RoleList
	var bindings rbacv1.RoleBindingList
	var opts []client.ListOption
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := k8sClient.List(ctx, &roles, opts...); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.List(ctx, &bindings, opts...); err != nil {
		t.Fatal(err)
	}
	nRoles, nBindings := 0, 0
	for i := range roles.Items {
		if roles.Items[i].Labels[LabelPool] == label {
			nRoles++
		}
	}
	for i := range bindings.Items {
		if bindings.Items[i].Labels[LabelPool] == label {
			nBindings++
		}
	}
	return nRoles, nBindings
}

func TestGuards(t *testing.T) {
	requireEnvtest(t)
	ctx, logs := captureLogs(t)
	poolNS := makeNS(t, ctx, "p")
	taskNS := makeNS(t, ctx, "t")
	rec := record.NewFakeRecorder(16)

	denied := testPool(poolNS, taskNS, 1)
	denied.Name = "denied"
	if err := k8sClient.Create(ctx, denied); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("factory-runs")
	r := &Reconciler{Client: k8sClient, Recorder: rec, Config: cfg}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: poolNS, Name: denied.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := &factoryv1.WorkerPool{}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond := readyCondition(t, got)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "TaskNamespaceNotAllowed" {
		t.Fatalf("denied condition: %+v", cond)
	}
	assertNone(t, ctx, poolNS, taskNS)

	missingNS := makeNS(t, ctx, "m")
	missing := testPool(missingNS, taskNS, 1)
	missing.Name = "missing"
	if err := k8sClient.Create(ctx, missing); err != nil {
		t.Fatal(err)
	}
	bad := testConfig(taskNS)
	bad.Image = ""
	r.Config = bad
	req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: missingNS, Name: missing.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond = readyCondition(t, got)
	if cond.Status != metav1.ConditionFalse || cond.Reason != "WORKER_IMAGE" || !strings.Contains(cond.Message, "WORKER_IMAGE") {
		t.Fatalf("missing config condition: %+v", cond)
	}
	assertNone(t, ctx, missingNS, taskNS)

	preNS := makeNS(t, ctx, "e")
	preTask := makeNS(t, ctx, "f")
	pre := testPool(preNS, preTask, 2)
	pre.Name = "pre"
	if err := k8sClient.Create(ctx, pre); err != nil {
		t.Fatal(err)
	}
	r.Config = testConfig(preTask)
	stack, err := Build(pre, r.Config)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: stack.Deployment.Name, Namespace: preNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32ptr(7),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foreign"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foreign"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "pause"}}},
			},
		},
	}
	if err := k8sClient.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: preNS, Name: pre.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	mustGet(t, ctx, client.ObjectKeyFromObject(foreign), foreign)
	if foreign.Spec.Replicas == nil || *foreign.Spec.Replicas != 7 || len(foreign.OwnerReferences) != 0 || foreign.Labels[LabelPool] != "" {
		t.Fatalf("pre-existing deployment was adopted: replicas %v owners %d labels %v", foreign.Spec.Replicas, len(foreign.OwnerReferences), foreign.Labels)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond = readyCondition(t, got)
	if cond.Reason != "PreexistingObject" {
		t.Fatalf("pre-existing condition: %+v", cond)
	}
	if strings.Contains(logs.String(), "sk-") {
		t.Fatal("guard logs contained a token-shaped value")
	}
}

func TestPlantedSecretStaysOutOfStatusEventsAndLogs(t *testing.T) {
	requireEnvtest(t)
	ctx, logs := captureLogs(t)
	rec := record.NewFakeRecorder(16)
	poolNS := makeNS(t, ctx, "s")
	const (
		plantedLabel = "sk-plantedPoolLabelSecret99"
		plantedAnn   = "ghp_plantedPoolAnnotationSecret99"
		plantedNS    = "sk-plantednssecret99"
		plantedExtra = "sk-plantedExtraAnnotationSecret99"
	)
	pool := testPool(poolNS, plantedNS, 1)
	pool.Name = "secret"
	pool.Labels = map[string]string{"tier": plantedLabel}
	pool.Annotations = map[string]string{"note": plantedAnn}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: k8sClient, Recorder: rec, Config: testConfig("factory-runs")}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: poolNS, Name: pool.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := &factoryv1.WorkerPool{}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	assertClean(t, got.Status, eventText(rec), logs.String(), plantedLabel, plantedAnn, plantedNS)

	okNS := makeNS(t, ctx, "o")
	taskNS := makeNS(t, ctx, "k")
	ok := testPool(okNS, taskNS, 1)
	ok.Name = "rendered"
	ok.Labels = map[string]string{"tier": plantedLabel}
	ok.Annotations = map[string]string{"note": plantedAnn}
	ok.Spec.KubernetesBackend.ExtraAnnotations = map[string]string{"example.com/note": plantedExtra}
	if err := k8sClient.Create(ctx, ok); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	r.Config = testConfig(taskNS)
	r.Recorder = record.NewFakeRecorder(16)
	req = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: okNS, Name: ok.Name}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	assertClean(t, got.Status, eventText(r.Recorder.(*record.FakeRecorder)), logs.String(), plantedLabel, plantedAnn, plantedExtra)
}

func testConfig(taskNS string) Config {
	cfg := fixtureConfig()
	cfg.TaskNamespaces = []string{taskNS}
	cfg.ImagePullSecrets = nil
	return cfg
}

func testPool(namespace, taskNS string, replicas int32) *factoryv1.WorkerPool {
	return &factoryv1.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: namespace},
		Spec: factoryv1.WorkerPoolSpec{
			Backend:            "kubernetes",
			Replicas:           replicas,
			MaxConcurrentTasks: 4,
			KubernetesBackend: &factoryv1.KubernetesBackendSpec{
				Namespace:            taskNS,
				DefaultImage:         "ubuntu:22.04",
				ImagePullPolicy:      "IfNotPresent",
				UnschedulableTimeout: "10m",
			},
		},
	}
}

func makeNS(t *testing.T, ctx context.Context, prefix string) string {
	t.Helper()
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	name := prefix + hex.EncodeToString(entropy[:])
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	return name
}

func mustGet(t *testing.T, ctx context.Context, key types.NamespacedName, obj client.Object) {
	t.Helper()
	if err := k8sClient.Get(ctx, key, obj); err != nil {
		t.Fatalf("get %T %s: %v", obj, key, err)
	}
}

func assertOwner(t *testing.T, pool *factoryv1.WorkerPool, objs ...client.Object) {
	t.Helper()
	for _, obj := range objs {
		refs := obj.GetOwnerReferences()
		if len(refs) != 1 {
			t.Fatalf("%s owner refs = %d", obj.GetName(), len(refs))
		}
		ref := refs[0]
		if ref.Kind != "WorkerPool" || ref.Name != pool.Name || ref.UID != pool.UID {
			t.Fatalf("%s owner = %+v", obj.GetName(), ref)
		}
		if ref.Controller == nil || !*ref.Controller || ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
			t.Fatalf("%s owner is not a blocking controller: %+v", obj.GetName(), ref)
		}
	}
}

func readyCondition(t *testing.T, pool *factoryv1.WorkerPool) metav1.Condition {
	t.Helper()
	for _, cond := range pool.Status.Conditions {
		if cond.Type == condReady {
			return cond
		}
	}
	t.Fatal("Ready condition missing")
	return metav1.Condition{}
}

func assertNone(t *testing.T, ctx context.Context, poolNS, taskNS string) {
	t.Helper()
	var deps appsv1.DeploymentList
	var sas corev1.ServiceAccountList
	var cms corev1.ConfigMapList
	var roles rbacv1.RoleList
	var bindings rbacv1.RoleBindingList
	if err := k8sClient.List(ctx, &deps, client.InNamespace(poolNS)); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.List(ctx, &sas, client.InNamespace(poolNS)); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.List(ctx, &cms, client.InNamespace(poolNS)); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.List(ctx, &roles, client.InNamespace(taskNS)); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.List(ctx, &bindings, client.InNamespace(taskNS)); err != nil {
		t.Fatal(err)
	}
	if len(deps.Items)+len(sas.Items)+len(cms.Items)+len(roles.Items)+len(bindings.Items) != 0 {
		t.Fatalf("guard created objects: deploy %d sa %d cm %d role %d binding %d", len(deps.Items), len(sas.Items), len(cms.Items), len(roles.Items), len(bindings.Items))
	}
}

func captureLogs(t *testing.T) (context.Context, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := funcr.New(func(prefix, args string) {
		fmt.Fprintf(&buf, "%s %s\n", prefix, args)
	}, funcr.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return logr.NewContext(ctx, logger), &buf
}

func eventText(rec *record.FakeRecorder) string {
	var b strings.Builder
	for {
		select {
		case ev := <-rec.Events:
			b.WriteString(ev)
			b.WriteByte('\n')
		default:
			return b.String()
		}
	}
}

func assertClean(t *testing.T, status factoryv1.WorkerPoolStatus, events, logs string, planted ...string) {
	t.Helper()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(raw) + "\n" + events + "\n" + logs
	for _, secret := range planted {
		if strings.Contains(blob, secret) {
			t.Fatal("status, event, or log contained a planted secret-shaped value")
		}
	}
	if status.ConnectedWorkers != 0 || status.InFlight != 0 || status.QueueDepth != 0 || status.RejectRate1m != 0 {
		t.Fatalf("capacity status was set: %+v", status)
	}
}

func int32ptr(v int32) *int32 { return &v }
