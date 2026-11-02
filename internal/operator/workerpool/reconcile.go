package workerpool

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	ctrlog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/operator"
)

const (
	finalizerName = "factory.dev/workerpool-cleanup"
	condReady     = "Ready"
	requeuePeriod = time.Minute
)

// Reconciler owns one worker stack per WorkerPool.
type Reconciler struct {
	client.Client
	// Live reads bypass the watch cache. Task-namespace Roles are not in it.
	Live     client.Reader
	Recorder record.EventRecorder
	Config   Config
}

// SetupWithManager registers the WorkerPool reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Live == nil {
		r.Live = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		// Legacy recorder: tests use record.FakeRecorder, same as the other reconcilers.
		r.Recorder = mgr.GetEventRecorderFor("workerpool") //nolint:staticcheck // SA1019
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1.WorkerPool{}, builder.WithPredicates(poolPredicate())).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func poolPredicate() predicate.Predicate {
	gen := predicate.GenerationChangedPredicate{}
	return predicate.Funcs{
		CreateFunc:  gen.Create,
		DeleteFunc:  gen.Delete,
		GenericFunc: gen.Generic,
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			if !e.ObjectOld.GetDeletionTimestamp().Equal(e.ObjectNew.GetDeletionTimestamp()) {
				return true
			}
			return gen.Update(e)
		},
	}
}

func (r *Reconciler) reader() client.Reader {
	if r.Live != nil {
		return r.Live
	}
	return r.Client
}

// Reconcile creates or repairs the worker stack, and deletes task-namespace
// RBAC before the pool's finalizer is released. envtest has no garbage
// collector; same-namespace objects are left for owner references.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pool factoryv1.WorkerPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !pool.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &pool)
	}
	if controllerutil.AddFinalizer(&pool, finalizerName) {
		if err := r.Update(ctx, &pool); err != nil {
			return ctrl.Result{}, err
		}
	}
	if missing := r.Config.Missing(); missing != "" {
		msg := "missing operator config " + missing
		if err := r.writeStatus(ctx, req.NamespacedName, metav1.ConditionFalse, missing, msg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	taskNS := ""
	if pool.Spec.KubernetesBackend != nil {
		taskNS = pool.Spec.KubernetesBackend.Namespace
	}
	if !r.Config.allows(taskNS) {
		// A move into a disallowed namespace must not leave the previous
		// namespace's labeled RBAC behind. The new namespace is not rendered.
		if err := r.dropPreviousTaskRBAC(ctx, &pool, taskNS); err != nil {
			return ctrl.Result{}, err
		}
		msg := fmt.Sprintf("kubernetesBackend.namespace %q is not in WORKERPOOL_TASK_NAMESPACES", taskNS)
		if err := r.writeStatus(ctx, req.NamespacedName, metav1.ConditionFalse, "TaskNamespaceNotAllowed", msg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	stack, err := Build(&pool, r.Config)
	if err != nil {
		return ctrl.Result{}, err
	}
	preexisting, err := r.applyStack(ctx, &pool, stack)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.noteTaskNamespace(ctx, req.NamespacedName, taskNS); err != nil {
		return ctrl.Result{}, err
	}
	if preexisting != "" {
		msg := preexisting + " exists without label " + LabelPool
		if err := r.writeStatus(ctx, req.NamespacedName, metav1.ConditionFalse, "PreexistingObject", msg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeuePeriod}, nil
	}
	ready, avail, err := r.deploymentReady(ctx, stack.Deployment, pool.Spec.Replicas)
	if err != nil {
		return ctrl.Result{}, err
	}
	status := metav1.ConditionFalse
	reason := "Progressing"
	msg := fmt.Sprintf("deployment available replicas %d, want %d", avail, pool.Spec.Replicas)
	if ready {
		status = metav1.ConditionTrue
		reason = "Ready"
		msg = "deployment available replicas match spec.replicas"
	}
	if err := r.writeStatus(ctx, req.NamespacedName, status, reason, msg); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeuePeriod}, nil
}

func (r *Reconciler) reconcileDelete(ctx context.Context, pool *factoryv1.WorkerPool) (ctrl.Result, error) {
	if err := r.deleteTaskRBAC(ctx, pool); err != nil {
		return ctrl.Result{}, err
	}
	if controllerutil.RemoveFinalizer(pool, finalizerName) {
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
	}
	ctrlog.FromContext(ctx).Info("workerpool finalizer released", "namespace", pool.Namespace, "name", pool.Name)
	return ctrl.Result{}, nil
}

func (r *Reconciler) deleteTaskRBAC(ctx context.Context, pool *factoryv1.WorkerPool) error {
	seen := map[string]struct{}{}
	var namespaces []string
	add := func(ns string) {
		if ns == "" {
			return
		}
		if _, ok := seen[ns]; ok {
			return
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	if pool.Annotations != nil {
		add(pool.Annotations[AnnTaskNamespace])
	}
	if pool.Spec.KubernetesBackend != nil {
		add(pool.Spec.KubernetesBackend.Namespace)
	}
	for _, ns := range namespaces {
		if err := r.deleteTaskRBACIn(ctx, pool, ns); err != nil {
			return err
		}
	}
	return nil
}

// dropPreviousTaskRBAC deletes this pool's labeled Role and RoleBinding in the
// namespace recorded by AnnTaskNamespace when spec now names a different one.
// The caller writes the new annotation only after this returns nil.
func (r *Reconciler) dropPreviousTaskRBAC(ctx context.Context, pool *factoryv1.WorkerPool, next string) error {
	prev := ""
	if pool.Annotations != nil {
		prev = pool.Annotations[AnnTaskNamespace]
	}
	if prev == "" || prev == next {
		return nil
	}
	return r.deleteTaskRBACIn(ctx, pool, prev)
}

func (r *Reconciler) deleteTaskRBACIn(ctx context.Context, pool *factoryv1.WorkerPool, namespace string) error {
	if namespace == "" {
		return nil
	}
	name := r.Config.objectName(pool.Name)
	if err := r.deleteIfOurs(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}, pool); err != nil {
		return err
	}
	return r.deleteIfOurs(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}, pool)
}

func (r *Reconciler) deleteIfOurs(ctx context.Context, obj client.Object, pool *factoryv1.WorkerPool) error {
	key := client.ObjectKeyFromObject(obj)
	existing, err := r.emptyLike(obj)
	if err != nil {
		return err
	}
	if err := r.reader().Get(ctx, key, existing); err != nil {
		return client.IgnoreNotFound(err)
	}
	if existing.GetLabels()[LabelPool] != PoolLabel(pool.Namespace, pool.Name) {
		return nil
	}
	if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *Reconciler) noteTaskNamespace(ctx context.Context, key types.NamespacedName, taskNS string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh factoryv1.WorkerPool
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		prev := ""
		if fresh.Annotations != nil {
			prev = fresh.Annotations[AnnTaskNamespace]
		}
		if prev == taskNS {
			return nil
		}
		// Delete the previous namespace before the annotation moves, or a
		// later delete only sees the new namespace.
		if err := r.dropPreviousTaskRBAC(ctx, &fresh, taskNS); err != nil {
			return err
		}
		ann := fresh.Annotations
		if ann == nil {
			ann = map[string]string{}
		}
		ann[AnnTaskNamespace] = taskNS
		fresh.Annotations = ann
		return r.Update(ctx, &fresh)
	})
}

func (r *Reconciler) applyStack(ctx context.Context, pool *factoryv1.WorkerPool, stack *Stack) (string, error) {
	type item struct {
		obj    client.Object
		sameNS bool
	}
	items := []item{
		{stack.ServiceAccount, true},
		{stack.ConfigMap, true},
		{stack.Role, false},
		{stack.RoleBinding, false},
		{stack.Deployment, true},
	}
	var preexisting string
	for _, item := range items {
		owned, err := r.apply(ctx, pool, item.obj, item.sameNS)
		if err != nil {
			return "", err
		}
		if !owned && preexisting == "" {
			preexisting = item.obj.GetObjectKind().GroupVersionKind().Kind
			if preexisting == "" {
				preexisting = kindOf(item.obj)
			}
			preexisting = fmt.Sprintf("%s %s/%s", preexisting, item.obj.GetNamespace(), item.obj.GetName())
		}
	}
	return preexisting, nil
}

func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *corev1.ServiceAccount:
		return "ServiceAccount"
	case *corev1.ConfigMap:
		return "ConfigMap"
	case *appsv1.Deployment:
		return "Deployment"
	case *rbacv1.Role:
		return "Role"
	case *rbacv1.RoleBinding:
		return "RoleBinding"
	default:
		return "Object"
	}
}

// apply creates or repairs obj. owned is false when a same-named object
// exists without this pool's label; that object is left unchanged.
func (r *Reconciler) apply(ctx context.Context, pool *factoryv1.WorkerPool, desired client.Object, sameNS bool) (bool, error) {
	if sameNS {
		if err := controllerutil.SetControllerReference(pool, desired, operator.Scheme); err != nil {
			return false, err
		}
	}
	key := client.ObjectKeyFromObject(desired)
	existing, err := r.emptyLike(desired)
	if err != nil {
		return false, err
	}
	if err := r.reader().Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, err
	}
	if existing.GetLabels()[LabelPool] != PoolLabel(pool.Namespace, pool.Name) {
		return false, nil
	}
	if ownedMatches(existing, desired) {
		return true, nil
	}
	mergeOwned(existing, desired)
	if err := r.Update(ctx, existing); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Reconciler) emptyLike(obj client.Object) (client.Object, error) {
	gvks, _, err := operator.Scheme.ObjectKinds(obj)
	if err != nil {
		return nil, err
	}
	if len(gvks) == 0 {
		return nil, fmt.Errorf("no group version kind")
	}
	fresh, err := operator.Scheme.New(gvks[0])
	if err != nil {
		return nil, err
	}
	out, ok := fresh.(client.Object)
	if !ok {
		return nil, fmt.Errorf("scheme object is %T", fresh)
	}
	return out, nil
}

func ownedMatches(existing, desired client.Object) bool {
	if !labelsMatch(existing.GetLabels(), desired.GetLabels()) {
		return false
	}
	if !labelsMatch(existing.GetAnnotations(), desired.GetAnnotations()) {
		return false
	}
	if !equality.Semantic.DeepEqual(existing.GetOwnerReferences(), desired.GetOwnerReferences()) {
		return false
	}
	switch want := desired.(type) {
	case *appsv1.Deployment:
		got, ok := existing.(*appsv1.Deployment)
		return ok && deploymentMatches(got, want)
	case *corev1.ConfigMap:
		got, ok := existing.(*corev1.ConfigMap)
		return ok && equality.Semantic.DeepEqual(got.Data, want.Data)
	case *rbacv1.Role:
		got, ok := existing.(*rbacv1.Role)
		return ok && equality.Semantic.DeepEqual(got.Rules, want.Rules)
	case *rbacv1.RoleBinding:
		got, ok := existing.(*rbacv1.RoleBinding)
		return ok && equality.Semantic.DeepEqual(got.Subjects, want.Subjects) && equality.Semantic.DeepEqual(got.RoleRef, want.RoleRef)
	case *corev1.ServiceAccount:
		_, ok := existing.(*corev1.ServiceAccount)
		return ok
	default:
		return false
	}
}

func labelsMatch(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func mergeOwned(existing, desired client.Object) {
	labels := existing.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range desired.GetLabels() {
		labels[k] = v
	}
	existing.SetLabels(labels)
	anns := existing.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	for k, v := range desired.GetAnnotations() {
		anns[k] = v
	}
	if len(anns) == 0 {
		existing.SetAnnotations(nil)
	} else {
		existing.SetAnnotations(anns)
	}
	existing.SetOwnerReferences(desired.GetOwnerReferences())
	switch want := desired.(type) {
	case *appsv1.Deployment:
		applyDeployment(existing.(*appsv1.Deployment), want)
	case *corev1.ConfigMap:
		existing.(*corev1.ConfigMap).Data = want.Data
	case *rbacv1.Role:
		existing.(*rbacv1.Role).Rules = want.Rules
	case *rbacv1.RoleBinding:
		got := existing.(*rbacv1.RoleBinding)
		got.Subjects = want.Subjects
		got.RoleRef = want.RoleRef
	case *corev1.ServiceAccount:
	default:
	}
}

// deploymentMatches ignores API defaults (DNS policy, rollout strategy, container
// termination message) so a reconcile does not fight the apiserver.
func deploymentMatches(got, want *appsv1.Deployment) bool {
	if got.Spec.Replicas == nil || want.Spec.Replicas == nil || *got.Spec.Replicas != *want.Spec.Replicas {
		return false
	}
	if got.Spec.Selector == nil || !equality.Semantic.DeepEqual(got.Spec.Selector.MatchLabels, want.Spec.Selector.MatchLabels) {
		return false
	}
	if !labelsMatch(got.Spec.Template.Labels, want.Spec.Template.Labels) {
		return false
	}
	if got.Spec.Template.Annotations[checksumAnn] != want.Spec.Template.Annotations[checksumAnn] {
		return false
	}
	gs, ws := got.Spec.Template.Spec, want.Spec.Template.Spec
	if gs.ServiceAccountName != ws.ServiceAccountName {
		return false
	}
	if gs.AutomountServiceAccountToken == nil || ws.AutomountServiceAccountToken == nil || *gs.AutomountServiceAccountToken != *ws.AutomountServiceAccountToken {
		return false
	}
	if gs.TerminationGracePeriodSeconds == nil || ws.TerminationGracePeriodSeconds == nil || *gs.TerminationGracePeriodSeconds != *ws.TerminationGracePeriodSeconds {
		return false
	}
	if !equality.Semantic.DeepEqual(gs.ImagePullSecrets, ws.ImagePullSecrets) {
		return false
	}
	if !equality.Semantic.DeepEqual(gs.SecurityContext, ws.SecurityContext) {
		return false
	}
	if !equality.Semantic.DeepEqual(gs.Volumes, ws.Volumes) {
		return false
	}
	if len(gs.Containers) != len(ws.Containers) {
		return false
	}
	for i := range ws.Containers {
		if !containerMatches(gs.Containers[i], ws.Containers[i]) {
			return false
		}
	}
	return true
}

func containerMatches(got, want corev1.Container) bool {
	if got.Name != want.Name || got.Image != want.Image || got.ImagePullPolicy != want.ImagePullPolicy {
		return false
	}
	if !equality.Semantic.DeepEqual(got.Args, want.Args) || !equality.Semantic.DeepEqual(got.Env, want.Env) {
		return false
	}
	if !equality.Semantic.DeepEqual(got.VolumeMounts, want.VolumeMounts) {
		return false
	}
	if !probeMatches(got.LivenessProbe, want.LivenessProbe) {
		return false
	}
	if !equality.Semantic.DeepEqual(got.SecurityContext, want.SecurityContext) {
		return false
	}
	return equality.Semantic.DeepEqual(got.Resources, want.Resources)
}

func probeMatches(got, want *corev1.Probe) bool {
	if got == nil || want == nil {
		return got == want
	}
	if got.InitialDelaySeconds != want.InitialDelaySeconds || got.PeriodSeconds != want.PeriodSeconds || got.FailureThreshold != want.FailureThreshold {
		return false
	}
	if got.Exec == nil || want.Exec == nil {
		return got.Exec == nil && want.Exec == nil
	}
	return equality.Semantic.DeepEqual(got.Exec.Command, want.Exec.Command)
}

func applyDeployment(got, want *appsv1.Deployment) {
	got.Spec.Replicas = want.Spec.Replicas
	if got.Spec.Selector == nil {
		got.Spec.Selector = want.Spec.Selector
	} else {
		got.Spec.Selector.MatchLabels = want.Spec.Selector.MatchLabels
	}
	if got.Spec.Template.Labels == nil {
		got.Spec.Template.Labels = map[string]string{}
	}
	for k, v := range want.Spec.Template.Labels {
		got.Spec.Template.Labels[k] = v
	}
	if got.Spec.Template.Annotations == nil {
		got.Spec.Template.Annotations = map[string]string{}
	}
	for k, v := range want.Spec.Template.Annotations {
		got.Spec.Template.Annotations[k] = v
	}
	spec := &got.Spec.Template.Spec
	spec.ServiceAccountName = want.Spec.Template.Spec.ServiceAccountName
	spec.AutomountServiceAccountToken = want.Spec.Template.Spec.AutomountServiceAccountToken
	spec.TerminationGracePeriodSeconds = want.Spec.Template.Spec.TerminationGracePeriodSeconds
	spec.ImagePullSecrets = want.Spec.Template.Spec.ImagePullSecrets
	spec.SecurityContext = want.Spec.Template.Spec.SecurityContext
	spec.Volumes = want.Spec.Template.Spec.Volumes
	if len(spec.Containers) == 0 {
		spec.Containers = want.Spec.Template.Spec.Containers
		return
	}
	for _, wantC := range want.Spec.Template.Spec.Containers {
		replaced := false
		for i := range spec.Containers {
			if spec.Containers[i].Name != wantC.Name {
				continue
			}
			keep := spec.Containers[i]
			keep.Image = wantC.Image
			keep.ImagePullPolicy = wantC.ImagePullPolicy
			keep.Args = wantC.Args
			keep.Env = wantC.Env
			keep.VolumeMounts = wantC.VolumeMounts
			keep.LivenessProbe = wantC.LivenessProbe
			keep.SecurityContext = wantC.SecurityContext
			keep.Resources = wantC.Resources
			spec.Containers[i] = keep
			replaced = true
			break
		}
		if !replaced {
			spec.Containers = append(spec.Containers, wantC)
		}
	}
}

func (r *Reconciler) deploymentReady(ctx context.Context, desired *appsv1.Deployment, replicas int32) (bool, int32, error) {
	var got appsv1.Deployment
	if err := r.reader().Get(ctx, client.ObjectKeyFromObject(desired), &got); err != nil {
		if apierrors.IsNotFound(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	avail := got.Status.AvailableReplicas
	return avail == replicas, avail, nil
}

func (r *Reconciler) writeStatus(ctx context.Context, key types.NamespacedName, status metav1.ConditionStatus, reason, message string) error {
	message = operator.Clip(message, 512)
	var pool factoryv1.WorkerPool
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, key, &pool); err != nil {
			return err
		}
		cond := metav1.Condition{
			Type:               condReady,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: pool.Generation,
		}
		before := append([]metav1.Condition(nil), pool.Status.Conditions...)
		operator.SetCondition(&pool.Status.Conditions, cond)
		changed = conditionChanged(before, cond)
		return r.Status().Update(ctx, &pool)
	})
	if err != nil {
		return err
	}
	ctrlog.FromContext(ctx).Info("workerpool status", "namespace", key.Namespace, "name", key.Name, "reason", reason, "message", message)
	if changed {
		r.event(&pool, status, reason, message)
	}
	return nil
}

func conditionChanged(before []metav1.Condition, next metav1.Condition) bool {
	for _, c := range before {
		if c.Type != next.Type {
			continue
		}
		return c.Status != next.Status || c.Reason != next.Reason || c.Message != next.Message || c.ObservedGeneration != next.ObservedGeneration
	}
	return true
}

func (r *Reconciler) event(obj runtime.Object, status metav1.ConditionStatus, reason, message string) {
	if r.Recorder == nil {
		return
	}
	eventType := corev1.EventTypeNormal
	if status != metav1.ConditionTrue {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(obj, eventType, reason, message)
}
