package workerpool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
)

// TestBuilderMatchesHelmRender compares the builder to testdata/helm-worker.yaml.
// The comparison normalizes only Helm-only metadata (helm.sh/chart,
// app.kubernetes.io/managed-by, and the helm.sh/hook, helm.sh/hook-weight,
// helm.sh/hook-delete-policy annotations) and the operator's owner references
// plus its factory.dev/workerpool label. Nothing else is rewritten.
func TestBuilderMatchesHelmRender(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/helm-worker.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := parseHelmStack(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Build(fixturePool(), fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	normalizeStack(got)
	normalizeStack(want)
	pairs := []struct {
		name string
		got  any
		want any
	}{
		{"ServiceAccount", got.ServiceAccount, want.ServiceAccount},
		{"ConfigMap", got.ConfigMap, want.ConfigMap},
		{"Deployment", got.Deployment, want.Deployment},
		{"Role", got.Role, want.Role},
		{"RoleBinding", got.RoleBinding, want.RoleBinding},
	}
	for _, pair := range pairs {
		if diff := jsonDiff(pair.got, pair.want); diff != "" {
			t.Errorf("%s differs from helm template\n%s", pair.name, diff)
		}
	}
}

func fixtureConfig() Config {
	return Config{
		Image:            "ghcr.io/acme/oz-agent-worker:test",
		ImagePullPolicy:  "IfNotPresent",
		WebSocketURL:     "ws://factory-factoryd.factory.svc:8080/api/v1/selfhosted/worker/ws",
		ServerRootURL:    "http://factory-factoryd.factory.svc:8080",
		APIKeySecretName: "factory-worker",
		APIKeySecretKey:  "FACTORY_API_KEY",
		TaskNamespaces:   []string{"factory-runs"},
		FullName:         "factory",
		AppName:          "factory",
		ReleaseName:      "factory",
		ImagePullSecrets: []string{"ghcr-pull"},
	}
}

func fixturePool() *factoryv1.WorkerPool {
	return &factoryv1.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "standard", Namespace: "factory"},
		Spec: factoryv1.WorkerPoolSpec{
			Backend:            "kubernetes",
			Replicas:           2,
			MaxConcurrentTasks: 8,
			KubernetesBackend: &factoryv1.KubernetesBackendSpec{
				Namespace:               "factory-runs",
				DefaultImage:            "ubuntu:22.04",
				ImagePullPolicy:         "IfNotPresent",
				ActiveDeadlineSeconds:   28800,
				TTLSecondsAfterFinished: 86400,
				WorkspaceSizeLimit:      "20Gi",
				UnschedulableTimeout:    "10m",
				ExtraAnnotations: map[string]string{
					"example.com/keep":            "keep",
					"karpenter.sh/do-not-disrupt": "true",
				},
				PodTemplate: &factoryv1.WorkerPodTemplate{
					ServiceAccountName: "factory-task",
					NodeSelector:       map[string]string{"factory.dev/pool": "standard"},
					Containers: []factoryv1.WorkerContainer{{
						Name: "task",
						Resources: &factoryv1.ContainerResources{
							Requests: map[string]string{"cpu": "1", "memory": "2Gi"},
							Limits:   map[string]string{"memory": "4Gi"},
						},
					}},
				},
			},
		},
	}
}

func parseHelmStack(raw []byte) (*Stack, error) {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	stack := &Stack{}
	for {
		var obj unstructured.Unstructured
		err := dec.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if obj.GetKind() == "" {
			continue
		}
		switch obj.GetKind() {
		case "ServiceAccount":
			stack.ServiceAccount = &corev1.ServiceAccount{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, stack.ServiceAccount); err != nil {
				return nil, err
			}
		case "ConfigMap":
			stack.ConfigMap = &corev1.ConfigMap{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, stack.ConfigMap); err != nil {
				return nil, err
			}
		case "Deployment":
			stack.Deployment = &appsv1.Deployment{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, stack.Deployment); err != nil {
				return nil, err
			}
		case "Role":
			stack.Role = &rbacv1.Role{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, stack.Role); err != nil {
				return nil, err
			}
		case "RoleBinding":
			stack.RoleBinding = &rbacv1.RoleBinding{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, stack.RoleBinding); err != nil {
				return nil, err
			}
		default:
		}
	}
	if stack.ServiceAccount == nil || stack.ConfigMap == nil || stack.Deployment == nil || stack.Role == nil || stack.RoleBinding == nil {
		return nil, errors.New("helm render is missing one of the five worker objects")
	}
	return stack, nil
}

func normalizeStack(stack *Stack) {
	for _, obj := range []metav1.Object{stack.ServiceAccount, stack.ConfigMap, stack.Deployment, stack.Role, stack.RoleBinding} {
		dropKeys(obj.GetLabels(), "helm.sh/chart", "app.kubernetes.io/managed-by", LabelPool)
		if len(obj.GetLabels()) == 0 {
			obj.SetLabels(nil)
		}
		dropKeys(obj.GetAnnotations(), "helm.sh/hook", "helm.sh/hook-weight", "helm.sh/hook-delete-policy")
		if len(obj.GetAnnotations()) == 0 {
			obj.SetAnnotations(nil)
		}
		obj.SetOwnerReferences(nil)
		obj.SetResourceVersion("")
		obj.SetUID("")
		obj.SetGeneration(0)
		obj.SetManagedFields(nil)
	}
}

func dropKeys(values map[string]string, keys ...string) {
	for _, key := range keys {
		delete(values, key)
	}
}

func jsonDiff(got, want any) string {
	a, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		return err.Error()
	}
	b, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		return err.Error()
	}
	if bytes.Equal(a, b) {
		return ""
	}
	return string(a) + "\n--- helm ---\n" + string(b)
}
