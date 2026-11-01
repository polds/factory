package operator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
)

const (
	finalizerStopRun    = "factory.dev/stop-run"
	condCancelRequested = "CancelRequested"
	condCancelTimedOut  = "CancelTimedOut"
	cancelGrace         = 120 * time.Second
)

// RunAPI is the factoryd run surface AgentRun uses.
type RunAPI interface {
	StartRun(ctx context.Context, req fclient.RunRequest) (fclient.Run, error)
	CancelRun(ctx context.Context, runID string) (fclient.Run, error)
	StreamRunEvents(ctx context.Context, runID string, yield func(fclient.Event) error) error
}

// AgentRunReconciler starts a run, mirrors its event stream, and cancels it on delete.
type AgentRunReconciler struct {
	client.Client
	API      RunAPI
	Recorder record.EventRecorder
	Log      logr.Logger
	Clock    Clock
	Grace    time.Duration
	// Base is the parent context for event watches. It outlives a single reconcile.
	Base context.Context

	mu      sync.Mutex
	watches map[string]context.CancelFunc
	started map[string]string
}

// SetupWithManager registers the AgentRun reconciler.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1.AgentRun{}, builder.WithPredicates(agentRunPredicate())).
		Complete(r)
}

func agentRunPredicate() predicate.Predicate {
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

func (r *AgentRunReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

func (r *AgentRunReconciler) grace() time.Duration {
	if r.Grace > 0 {
		return r.Grace
	}
	return cancelGrace
}

func (r *AgentRunReconciler) log() logr.Logger {
	if r.Log.GetSink() != nil {
		return r.Log
	}
	return ctrl.Log
}

// Reconcile starts an AgentRun once and removes its finalizer after cancel.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run factoryv1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, req.NamespacedName, &run)
	}
	if controllerutil.AddFinalizer(&run, finalizerStopRun) {
		if err := r.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}
	if run.Status.RunID == "" {
		if id := r.remembered(req.NamespacedName); id != "" {
			if err := r.writeRunID(ctx, req.NamespacedName, id, ""); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			started, err := r.API.StartRun(ctx, runRequest(&run))
			if err != nil {
				msg := clip(err.Error(), maxMessage)
				r.log().Info("run start failed", "agentrun", req.NamespacedName.String(), "message", msg)
				r.event(&run, corev1.EventTypeWarning, "StartFailed", msg)
				return ctrl.Result{}, err
			}
			r.remember(req.NamespacedName, started.ID)
			phase := redact(started.Status)
			if err := r.writeRunID(ctx, req.NamespacedName, started.ID, phase); err != nil {
				return ctrl.Result{}, err
			}
			r.log().Info("run started", "agentrun", req.NamespacedName.String(), "phase", phase)
			r.event(&run, corev1.EventTypeNormal, "Started", "run started")
		}
	}
	var fresh factoryv1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	r.ensureWatch(req.NamespacedName, fresh.Status.RunID)
	return ctrl.Result{}, nil
}

func runRequest(run *factoryv1.AgentRun) fclient.RunRequest {
	req := fclient.RunRequest{Prompt: run.Spec.Prompt, Priority: run.Spec.Priority}
	if run.Spec.FactoryRef != nil {
		req.Factory = run.Spec.FactoryRef.Name
	}
	if run.Spec.AgentRef != nil {
		req.Agent = run.Spec.AgentRef.Name
	}
	return req
}

func (r *AgentRunReconciler) reconcileDelete(ctx context.Context, key types.NamespacedName, run *factoryv1.AgentRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, finalizerStopRun) {
		r.stopWatch(key)
		return ctrl.Result{}, nil
	}
	if run.Status.RunID == "" {
		r.stopWatch(key)
		return ctrl.Result{}, r.removeFinalizer(ctx, key)
	}
	if !conditionTrue(run.Status.Conditions, condCancelRequested) {
		cancelled, err := r.API.CancelRun(ctx, run.Status.RunID)
		if err != nil {
			msg := clip(err.Error(), maxMessage)
			r.log().Info("run cancel failed", "agentrun", key.String(), "message", msg)
			return ctrl.Result{}, err
		}
		phase := ""
		if cancelled.Status != "" {
			phase = redact(cancelled.Status)
		}
		if err := r.markCancelRequested(ctx, key, phase); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, key, run); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	if terminalPhase(run.Status.Phase) {
		r.stopWatch(key)
		r.event(run, corev1.EventTypeNormal, "Cancelled", "run is terminal")
		return ctrl.Result{}, r.removeFinalizer(ctx, key)
	}
	requested := conditionTime(run.Status.Conditions, condCancelRequested)
	if !requested.IsZero() && r.now().Sub(requested) >= r.grace() {
		if !conditionTrue(run.Status.Conditions, condCancelTimedOut) {
			if err := r.markTimedOut(ctx, key); err != nil {
				return ctrl.Result{}, err
			}
			r.event(run, corev1.EventTypeWarning, "CancelTimedOut", "cancel grace elapsed")
			return ctrl.Result{Requeue: true}, nil
		}
		r.stopWatch(key)
		return ctrl.Result{}, r.removeFinalizer(ctx, key)
	}
	wait := r.grace()
	if !requested.IsZero() {
		wait = r.grace() - r.now().Sub(requested)
		if wait < time.Second {
			wait = time.Second
		}
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

func (r *AgentRunReconciler) ensureWatch(key types.NamespacedName, runID string) {
	if runID == "" || r.API == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches == nil {
		r.watches = map[string]context.CancelFunc{}
	}
	id := key.String()
	if _, ok := r.watches[id]; ok {
		return
	}
	parent := r.Base
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	r.watches[id] = cancel
	go r.watch(ctx, key, runID)
}

func (r *AgentRunReconciler) stopWatch(key types.NamespacedName) {
	r.mu.Lock()
	cancel := r.watches[key.String()]
	delete(r.watches, key.String())
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *AgentRunReconciler) watch(ctx context.Context, key types.NamespacedName, runID string) {
	defer r.dropWatch(key)
	err := r.API.StreamRunEvents(ctx, runID, func(ev fclient.Event) error {
		return r.applyEvent(ctx, key, ev)
	})
	if err != nil && ctx.Err() == nil {
		r.log().Info("run event stream ended", "agentrun", key.String())
	}
}

func (r *AgentRunReconciler) dropWatch(key types.NamespacedName) {
	r.mu.Lock()
	delete(r.watches, key.String())
	r.mu.Unlock()
}

func (r *AgentRunReconciler) applyEvent(ctx context.Context, key types.NamespacedName, ev fclient.Event) error {
	fields := parseRunEvent(ev)
	if fields.phase == "" && len(fields.executions) == 0 && fields.usage == nil && len(fields.prs) == 0 {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run factoryv1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if fields.phase != "" {
			run.Status.Phase = fields.phase
		}
		if len(fields.executions) > 0 {
			run.Status.Executions = fields.executions
		}
		if fields.usage != nil {
			run.Status.Usage = fields.usage
		}
		if len(fields.prs) > 0 {
			run.Status.PRURLs = fields.prs
		}
		return r.Status().Update(ctx, &run)
	})
}

type parsedEvent struct {
	phase      string
	executions []factoryv1.AgentRunExecution
	usage      *factoryv1.AgentRunUsage
	prs        []string
}

func parseRunEvent(ev fclient.Event) parsedEvent {
	var raw struct {
		Status        string                        `json:"status"`
		Phase         string                        `json:"phase"`
		Executions    []factoryv1.AgentRunExecution `json:"executions"`
		Usage         *factoryv1.AgentRunUsage      `json:"usage"`
		PRURLs        []string                      `json:"prUrls"`
		PRURLsSnake   []string                      `json:"pr_urls"`
		WorkItemID    string                        `json:"workItemId"`
		WorkItemSnake string                        `json:"work_item_id"`
	}
	if ev.Data != "" {
		_ = json.Unmarshal([]byte(ev.Data), &raw)
	}
	phase := raw.Phase
	if phase == "" {
		phase = raw.Status
	}
	if phase == "" && (ev.Type == "terminal" || ev.Type == "run.terminal") {
		phase = "succeeded"
	}
	prs := raw.PRURLs
	if len(prs) == 0 {
		prs = raw.PRURLsSnake
	}
	return parsedEvent{
		phase:      redact(phase),
		executions: raw.Executions,
		usage:      raw.Usage,
		prs:        redactAll(prs),
	}
}

func redactAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact(s)
	}
	return out
}

func terminalPhase(phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "succeeded", "failed", "cancelled", "canceled", "complete", "completed":
		return true
	default:
		return false
	}
}

func (r *AgentRunReconciler) writeRunID(ctx context.Context, key types.NamespacedName, runID, phase string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run factoryv1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			return err
		}
		run.Status.RunID = runID
		if phase != "" {
			run.Status.Phase = phase
		}
		return r.Status().Update(ctx, &run)
	})
}

func (r *AgentRunReconciler) markCancelRequested(ctx context.Context, key types.NamespacedName, phase string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run factoryv1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			return err
		}
		if phase != "" && !terminalPhase(run.Status.Phase) {
			run.Status.Phase = phase
		}
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               condCancelRequested,
			Status:             metav1.ConditionTrue,
			Reason:             "CancelRequested",
			Message:            "cancellation was requested",
			ObservedGeneration: run.Generation,
			LastTransitionTime: metav1.NewTime(r.now()),
		})
		return r.Status().Update(ctx, &run)
	})
}

func (r *AgentRunReconciler) markTimedOut(ctx context.Context, key types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run factoryv1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			return err
		}
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               condCancelTimedOut,
			Status:             metav1.ConditionTrue,
			Reason:             "CancelTimedOut",
			Message:            "cancel grace elapsed",
			ObservedGeneration: run.Generation,
			LastTransitionTime: metav1.NewTime(r.now()),
		})
		return r.Status().Update(ctx, &run)
	})
}

func (r *AgentRunReconciler) removeFinalizer(ctx context.Context, key types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run factoryv1.AgentRun
		if err := r.Get(ctx, key, &run); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !controllerutil.RemoveFinalizer(&run, finalizerStopRun) {
			return nil
		}
		return r.Update(ctx, &run)
	})
}

func (r *AgentRunReconciler) remember(key types.NamespacedName, runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started == nil {
		r.started = map[string]string{}
	}
	r.started[key.String()] = runID
}

func (r *AgentRunReconciler) remembered(key types.NamespacedName) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started[key.String()]
}

func (r *AgentRunReconciler) event(obj client.Object, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(obj, eventType, reason, redact(message))
}

func conditionTrue(conds []metav1.Condition, typ string) bool {
	for _, c := range conds {
		if c.Type == typ && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func conditionTime(conds []metav1.Condition, typ string) time.Time {
	for _, c := range conds {
		if c.Type == typ {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}
