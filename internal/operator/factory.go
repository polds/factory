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
	GetDefinition(ctx context.Context, factory string) (api.DefinitionView, error)
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
	resp, appliedGen, err := r.pushDefinition(ctx, factory.Name, gen, got.files)
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
	if resp.Valid && appliedGen != gen {
		log.Info("definition publish retried", "factory", factory.Name, "generation", appliedGen)
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
	// A short-circuit leaves the previous generation serving and still
	// reports Valid. Record the hash only when that generation is the one
	// this reconcile sent and factoryd stored these bytes.
	if !valid || resp.PublishedGeneration != appliedGen {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.writeAnnotations(ctx, req.NamespacedName, appliedGen, hash)
}

// pushDefinition publishes files. factoryd answers Valid without storing
// the body when the generation is already served. A different generation
// is pushed again at PublishedGeneration+1. The same generation with
// different bytes is not treated as stored; an identical body is.
func (r *FactoryReconciler) pushDefinition(ctx context.Context, name string, gen int64, files []api.DefinitionFile) (fclient.DefinitionPushResponse, int64, error) {
	var resp fclient.DefinitionPushResponse
	sent := gen
	for range 3 {
		var err error
		resp, err = r.API.PushDefinition(ctx, name, fclient.DefinitionPushRequest{
			Generation: sent,
			Files:      files,
		})
		if err != nil || !resp.Valid {
			return resp, sent, err
		}
		if resp.PublishedGeneration != sent {
			sent = resp.PublishedGeneration + 1
			continue
		}
		match, err := r.servedMatches(ctx, name, files)
		if err != nil {
			return resp, sent, err
		}
		if match {
			return resp, sent, nil
		}
		sent = resp.PublishedGeneration + 1
	}
	return resp, sent, fmt.Errorf("publish %s: generation %d was not applied", name, sent)
}

func (r *FactoryReconciler) servedMatches(ctx context.Context, name string, files []api.DefinitionFile) (bool, error) {
	view, err := r.API.GetDefinition(ctx, name)
	if err != nil {
		return false, err
	}
	return bundleHash(view.Files) == bundleHash(files), nil
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

// SetCondition records c on conds. An unchanged condition keeps its transition time.
func SetCondition(conds *[]metav1.Condition, c metav1.Condition) {
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

// ownedItems lists one kind in the Factory's namespace and returns the items
// this Factory owns, in list order. items must be the address of list.Items
// so List fills the slice the filter reads. A List error is returned unwrapped.
func ownedItems[T any](ctx context.Context, r *FactoryReconciler, factory *factoryv1.Factory, list client.ObjectList, items *[]T, ref func(*T) *factoryv1.LocalRef) ([]T, error) {
	if err := r.List(ctx, list, client.InNamespace(factory.Namespace)); err != nil {
		return nil, err
	}
	var out []T
	for i := range *items {
		item := &(*items)[i]
		if owns(factory, ref(item), r, ctx) {
			out = append(out, *item)
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
