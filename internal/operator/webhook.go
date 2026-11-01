package operator

import (
	"context"
	"fmt"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
)

// Validator is the factory.dev validating admission webhook.
type Validator struct {
	client.Client
}

// SetupWithManager registers validating webhooks for the factory.dev kinds
// the cross-resource rules inspect.
func (v *Validator) SetupWithManager(mgr ctrl.Manager) error {
	if err := builder.WebhookManagedBy(mgr, &factoryv1.Factory{}).WithCustomValidator(v).Complete(); err != nil {
		return err
	}
	if err := registerFactoryAgent(mgr, v); err != nil {
		return err
	}
	if err := registerAutomation(mgr, v); err != nil {
		return err
	}
	if err := registerRunner(mgr, v); err != nil {
		return err
	}
	return registerScorer(mgr, v)
}

func registerFactoryAgent(mgr ctrl.Manager, v *Validator) error {
	return builder.WebhookManagedBy(mgr, &factoryv1.FactoryAgent{}).WithCustomValidator(v).Complete()
}

func registerAutomation(mgr ctrl.Manager, v *Validator) error {
	return builder.WebhookManagedBy(mgr, &factoryv1.Automation{}).WithCustomValidator(v).Complete()
}

func registerRunner(mgr ctrl.Manager, v *Validator) error {
	return builder.WebhookManagedBy(mgr, &factoryv1.Runner{}).WithCustomValidator(v).Complete()
}

func registerScorer(mgr ctrl.Manager, v *Validator) error {
	return builder.WebhookManagedBy(mgr, &factoryv1.Scorer{}).WithCustomValidator(v).Complete()
}

// ValidateCreate admits a new object.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, v.admit(ctx, nil, obj)
}

// ValidateUpdate admits a changed object.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	return nil, v.admit(ctx, oldObj, newObj)
}

// ValidateDelete admits deletes so a namespace can finish cleanup.
func (v *Validator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *Validator) admit(ctx context.Context, oldObj, newObj runtime.Object) error {
	if err := v.runnerExists(ctx, newObj); err != nil {
		return err
	}
	if err := v.automationAgent(ctx, newObj); err != nil {
		return err
	}
	if err := v.oneForeman(ctx, newObj); err != nil {
		return err
	}
	return v.approvals(oldObj, newObj)
}

func (v *Validator) runnerExists(ctx context.Context, obj runtime.Object) error {
	for _, name := range runnerRefs(obj) {
		if strings.TrimSpace(name) == "" {
			continue
		}
		var runner factoryv1.Runner
		err := v.Get(ctx, client.ObjectKey{Namespace: namespaceOf(obj), Name: name}, &runner)
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("runner %q does not exist", redact(name))
		}
		if err != nil {
			return fmt.Errorf("runner lookup failed")
		}
	}
	return nil
}

func runnerRefs(obj runtime.Object) []string {
	switch o := obj.(type) {
	case *factoryv1.Factory:
		var names []string
		if o.Spec.AgentDefaults != nil {
			names = append(names, o.Spec.AgentDefaults.Runner)
		}
		if o.Spec.ScorerDefaults != nil {
			names = append(names, o.Spec.ScorerDefaults.Runner)
		}
		return names
	case *factoryv1.FactoryAgent:
		return []string{o.Spec.Runner}
	case *factoryv1.Automation:
		return []string{o.Spec.Runner}
	default:
		return nil
	}
}

func (v *Validator) automationAgent(ctx context.Context, obj runtime.Object) error {
	auto, ok := obj.(*factoryv1.Automation)
	if !ok {
		return nil
	}
	if auto.Spec.AgentRef == nil || strings.TrimSpace(auto.Spec.AgentRef.Name) == "" {
		return fmt.Errorf("automation agent is required")
	}
	var agent factoryv1.FactoryAgent
	err := v.Get(ctx, client.ObjectKey{Namespace: auto.Namespace, Name: auto.Spec.AgentRef.Name}, &agent)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("automation agent %q does not exist", redact(auto.Spec.AgentRef.Name))
	}
	if err != nil {
		return fmt.Errorf("automation agent lookup failed")
	}
	return nil
}

func (v *Validator) oneForeman(ctx context.Context, obj runtime.Object) error {
	agent, ok := obj.(*factoryv1.FactoryAgent)
	if !ok {
		return nil
	}
	var list factoryv1.FactoryAgentList
	if err := v.List(ctx, &list, client.InNamespace(agent.Namespace)); err != nil {
		return fmt.Errorf("agent list failed")
	}
	key := refName(agent.Spec.FactoryRef)
	count := 0
	seen := false
	for i := range list.Items {
		item := list.Items[i]
		if refName(item.Spec.FactoryRef) != key {
			continue
		}
		current := item
		if item.Name == agent.Name {
			current = *agent
			seen = true
		}
		if current.Spec.AgentType == "FOREMAN" {
			count++
		}
	}
	if !seen && agent.Spec.AgentType == "FOREMAN" {
		count++
	}
	if count != 1 {
		return fmt.Errorf("factory requires exactly one FOREMAN agent")
	}
	return nil
}

func (v *Validator) approvals(oldObj, newObj runtime.Object) error {
	next, ok := newObj.(*factoryv1.Factory)
	if !ok {
		return nil
	}
	var prev *factoryv1.Factory
	if old, ok := oldObj.(*factoryv1.Factory); ok {
		prev = old
	}
	base, err := factoryTree(prev)
	if err != nil {
		return fmt.Errorf("pipeline.approvals: %s", clip(err.Error(), maxMessage))
	}
	head, err := factoryTree(next)
	if err != nil {
		return fmt.Errorf("pipeline.approvals: %s", clip(err.Error(), maxMessage))
	}
	var msgs []string
	for _, diag := range definition.GuardRails(base, head) {
		if diag.Severity != definition.SeverityError {
			continue
		}
		msgs = append(msgs, "pipeline.approvals: "+diag.Message)
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", clip(strings.Join(msgs, "; "), maxMessage))
}

func factoryTree(factory *factoryv1.Factory) (*definition.Tree, error) {
	if factory == nil {
		return nil, nil
	}
	files, err := filesFromObject(factory, factory.Spec.Files)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "factory-admit-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	apiFiles := make([]api.DefinitionFile, len(files))
	copy(apiFiles, files)
	if err := writeAdmitFiles(dir, apiFiles); err != nil {
		return nil, err
	}
	tree, _ := definition.LoadDir(dir)
	return tree, nil
}

func writeAdmitFiles(dir string, files []api.DefinitionFile) error {
	for _, file := range files {
		target, err := admitPath(dir, file.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(parentDir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func parentDir(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path
	}
	return path[:i]
}

func admitPath(root, rel string) (string, error) {
	clean := strings.TrimPrefix(rel, "/")
	if clean == "" || strings.Contains(clean, "..") {
		return "", fmt.Errorf("definition path is invalid")
	}
	return root + "/" + clean, nil
}

func namespaceOf(obj runtime.Object) string {
	if o, ok := obj.(client.Object); ok {
		return o.GetNamespace()
	}
	return ""
}
