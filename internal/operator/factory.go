package operator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/api"
	fclient "github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/definition"
)

const (
	condDefinitionValid = "DefinitionValid"
	condReady           = "Ready"
	maxDiagnostics      = 8
	maxMessage          = 512
)

// DefinitionAPI is the factoryd definition push the Factory reconciler calls.
type DefinitionAPI interface {
	PushDefinition(ctx context.Context, factory string, req fclient.DefinitionPushRequest) (fclient.DefinitionPushResponse, error)
}

// FactoryReconciler publishes a Factory and its child kinds to factoryd.
type FactoryReconciler struct {
	client.Client
	API      DefinitionAPI
	Recorder record.EventRecorder
	Log      logr.Logger
}

// SetupWithManager registers the Factory reconciler and its child watches.
func (r *FactoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1.Factory{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	for _, obj := range childPrototypes() {
		b = b.Watches(obj, handler.EnqueueRequestsFromMapFunc(r.mapChild), builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	}
	return b.Complete(r)
}

func childPrototypes() []client.Object {
	return []client.Object{
		&factoryv1.FactoryAgent{},
		&factoryv1.Automation{},
		&factoryv1.Runner{},
		&factoryv1.ModelRouter{},
		&factoryv1.Scorer{},
		&factoryv1.Skill{},
		&factoryv1.FactoryWebhook{},
		&factoryv1.BenchmarkSuite{},
	}
}

func (r *FactoryReconciler) mapChild(ctx context.Context, obj client.Object) []reconcile.Request {
	if name := factoryRefName(obj); name != "" {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
	}
	var list factoryv1.FactoryList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil || len(list.Items) != 1 {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: list.Items[0].Name}}}
}

// Reconcile pushes the definition bundle and writes DefinitionValid and Ready.
func (r *FactoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log
	if log.GetSink() == nil {
		log = ctrl.Log
	}
	var factory factoryv1.Factory
	if err := r.Get(ctx, req.NamespacedName, &factory); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	got, err := r.bundle(ctx, &factory)
	if err != nil {
		msg := clip(err.Error(), maxMessage)
		log.Info("definition bundle rejected", "factory", factory.Name, "message", msg)
		r.event(&factory, corev1.EventTypeWarning, "Invalid", msg)
		return ctrl.Result{}, r.writeStatus(ctx, req.NamespacedName, factoryStatus(factory.Generation, "Invalid", got.counts, false, "Invalid", msg))
	}
	hash := bundleHash(got.files)
	prevHash := ""
	if factory.Annotations != nil {
		prevHash = factory.Annotations[annHash]
	}
	changed := hash != prevHash
	gen := pushGeneration(factory.Generation, annInt(&factory, annGeneration), changed)
	if !changed && statusCurrent(&factory, got.counts) {
		return ctrl.Result{}, nil
	}
	if r.API == nil {
		return ctrl.Result{}, errors.New("factory definition client is unset")
	}
	resp, err := r.API.PushDefinition(ctx, factory.Name, fclient.DefinitionPushRequest{
		Generation: gen,
		Files:      got.files,
	})
	if errors.Is(err, fclient.ErrGitBackedFactory) {
		msg := "factory definition is git-backed"
		r.event(&factory, corev1.EventTypeWarning, "GitBacked", msg)
		if writeErr := r.writeStatus(ctx, req.NamespacedName, factoryStatus(factory.Generation, "Invalid", got.counts, false, "GitBacked", msg)); writeErr != nil {
			return ctrl.Result{}, writeErr
		}
		return ctrl.Result{}, r.writeAnnotations(ctx, req.NamespacedName, gen, hash)
	}
	if err != nil {
		msg := clip(err.Error(), maxMessage)
		log.Info("definition publish failed", "factory", factory.Name, "message", msg)
		r.event(&factory, corev1.EventTypeWarning, "PublishFailed", msg)
		return ctrl.Result{}, fmt.Errorf("publish %s: %s", factory.Name, msg)
	}
	valid := resp.Valid
	reason := "Published"
	message := "definition published"
	phase := "Ready"
	if !valid {
		reason = "Invalid"
		phase = "Invalid"
		message = diagnosticMessage(resp.Errors)
	}
	message = clip(message, maxMessage)
	log.Info("definition publish result", "factory", factory.Name, "valid", valid)
	eventType := corev1.EventTypeNormal
	if !valid {
		eventType = corev1.EventTypeWarning
	}
	r.event(&factory, eventType, reason, message)
	status := factoryStatus(factory.Generation, phase, got.counts, valid, reason, message)
	if err := r.writeStatus(ctx, req.NamespacedName, status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.writeAnnotations(ctx, req.NamespacedName, gen, hash)
}

func (r *FactoryReconciler) event(obj client.Object, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(obj, eventType, reason, redact(message))
}

func factoryStatus(observed int64, phase string, counts factoryv1.FactoryResourceCounts, valid bool, reason, message string) factoryv1.FactoryStatus {
	status := metav1.ConditionFalse
	if valid {
		status = metav1.ConditionTrue
	}
	readyReason := reason
	readyMessage := message
	if valid {
		readyReason = "Ready"
		readyMessage = "factoryd is serving this definition"
	}
	out := factoryv1.FactoryStatus{
		Phase:              phase,
		ObservedGeneration: observed,
		Resources:          counts,
	}
	metaSet(&out.Conditions, condDefinitionValid, status, reason, message, observed)
	metaSet(&out.Conditions, condReady, status, readyReason, readyMessage, observed)
	return out
}

func metaSet(conds *[]metav1.Condition, typ string, status metav1.ConditionStatus, reason, message string, observed int64) {
	c := metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observed,
	}
	setCondition(conds, c)
}

func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	now := metav1.Now()
	for i := range *conds {
		if (*conds)[i].Type != c.Type {
			continue
		}
		prev := (*conds)[i]
		if prev.Status == c.Status && prev.Reason == c.Reason && prev.Message == c.Message && prev.ObservedGeneration == c.ObservedGeneration {
			return
		}
		c.LastTransitionTime = now
		(*conds)[i] = c
		return
	}
	if c.LastTransitionTime.IsZero() {
		c.LastTransitionTime = now
	}
	*conds = append(*conds, c)
}

func statusCurrent(factory *factoryv1.Factory, counts factoryv1.FactoryResourceCounts) bool {
	if factory.Status.ObservedGeneration != factory.Generation {
		return false
	}
	if factory.Status.Resources != counts {
		return false
	}
	return conditionSet(factory.Status.Conditions, condDefinitionValid) && conditionSet(factory.Status.Conditions, condReady)
}

func conditionSet(conds []metav1.Condition, typ string) bool {
	for _, c := range conds {
		if c.Type == typ && c.Reason != "" {
			return true
		}
	}
	return false
}

func diagnosticMessage(diags []api.Diagnostic) string {
	var parts []string
	for _, diag := range diags {
		if diag.Severity != "" && diag.Severity != definition.SeverityError {
			continue
		}
		parts = append(parts, redact(diag.Message))
		if len(parts) == maxDiagnostics {
			break
		}
	}
	if len(parts) == 0 {
		return "definition was rejected"
	}
	return strings.Join(parts, "; ")
}

func (r *FactoryReconciler) writeStatus(ctx context.Context, key types.NamespacedName, status factoryv1.FactoryStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh factoryv1.Factory
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		if fresh.Status.Phase == status.Phase &&
			fresh.Status.ObservedGeneration == status.ObservedGeneration &&
			fresh.Status.Resources == status.Resources &&
			conditionsEqual(fresh.Status.Conditions, status.Conditions) {
			return nil
		}
		fresh.Status = status
		return r.Status().Update(ctx, &fresh)
	})
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for _, want := range b {
		var got *metav1.Condition
		for i := range a {
			if a[i].Type == want.Type {
				got = &a[i]
				break
			}
		}
		if got == nil || got.Status != want.Status || got.Reason != want.Reason || got.Message != want.Message || got.ObservedGeneration != want.ObservedGeneration {
			return false
		}
	}
	return true
}

func (r *FactoryReconciler) writeAnnotations(ctx context.Context, key types.NamespacedName, gen int64, hash string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh factoryv1.Factory
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		base := fresh.DeepCopy()
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		genText := strconv.FormatInt(gen, 10)
		if fresh.Annotations[annGeneration] == genText && fresh.Annotations[annHash] == hash {
			return nil
		}
		fresh.Annotations[annGeneration] = genText
		fresh.Annotations[annHash] = hash
		return r.Patch(ctx, &fresh, client.MergeFrom(base))
	})
}

func (r *FactoryReconciler) agentsFor(ctx context.Context, factory *factoryv1.Factory) ([]factoryv1.FactoryAgent, error) {
	var list factoryv1.FactoryAgentList
	if err := r.List(ctx, &list, client.InNamespace(factory.Namespace)); err != nil {
		return nil, err
	}
	var out []factoryv1.FactoryAgent
	for i := range list.Items {
		if owns(factory, list.Items[i].Spec.FactoryRef, r, ctx) {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

func (r *FactoryReconciler) automationsFor(ctx context.Context, factory *factoryv1.Factory) ([]factoryv1.Automation, error) {
	var list factoryv1.AutomationList
	if err := r.List(ctx, &list, client.InNamespace(factory.Namespace)); err != nil {
		return nil, err
	}
	var out []factoryv1.Automation
	for i := range list.Items {
		if owns(factory, list.Items[i].Spec.FactoryRef, r, ctx) {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

func (r *FactoryReconciler) runnersFor(ctx context.Context, factory *factoryv1.Factory) ([]factoryv1.Runner, error) {
	var list factoryv1.RunnerList
	if err := r.List(ctx, &list, client.InNamespace(factory.Namespace)); err != nil {
		return nil, err
	}
	var out []factoryv1.Runner
	for i := range list.Items {
		if owns(factory, list.Items[i].Spec.FactoryRef, r, ctx) {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

func (r *FactoryReconciler) scorersFor(ctx context.Context, factory *factoryv1.Factory) ([]factoryv1.Scorer, error) {
	var list factoryv1.ScorerList
	if err := r.List(ctx, &list, client.InNamespace(factory.Namespace)); err != nil {
		return nil, err
	}
	var out []factoryv1.Scorer
	for i := range list.Items {
		if owns(factory, list.Items[i].Spec.FactoryRef, r, ctx) {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
}

func factoryRefName(obj client.Object) string {
	return refOf(obj)
}

func refName(ref *factoryv1.LocalRef) string {
	if ref == nil {
		return ""
	}
	return ref.Name
}

func refOf(obj client.Object) string {
	switch o := obj.(type) {
	case *factoryv1.FactoryAgent:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.Automation:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.Runner:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.ModelRouter:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.Scorer:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.Skill:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.FactoryWebhook:
		return refName(o.Spec.FactoryRef)
	case *factoryv1.BenchmarkSuite:
		return refName(o.Spec.FactoryRef)
	default:
		return ""
	}
}
