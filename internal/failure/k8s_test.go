package failure

import (
	"reflect"
	"testing"
)

// TestKubernetesFailureKinds is the classification table for the upstream
// Kubernetes failure reasons. Agent exit 10 stays agent. The rest are
// infrastructure or configuration, which is what eligibility excludes.
func TestKubernetesFailureKinds(t *testing.T) {
	byName := map[string]Rule{}
	for _, rule := range Cases() {
		byName[rule.Name] = rule
	}
	required := []string{
		"k8s_unschedulable",
		"k8s_image_pull",
		"k8s_preflight_rbac_job_create",
		"k8s_active_deadline",
		"k8s_active_deadline_sigterm",
		"evicted",
		"oom_killed",
		"k8s_container_exit_nonzero",
		"container_exit_code_1",
		"k8s_agent_exit_10",
		"k8s_job_watch",
		"k8s_pod_watch",
		"k8s_job_failed",
		"k8s_volume_mount",
		"k8s_init_container",
		"k8s_invalid_image",
		"k8s_container_create",
		"k8s_sidecar_prep",
		"k8s_container_wait",
		"k8s_task_cancelled",
		"k8s_graceful_shutdown",
	}
	for _, name := range required {
		rule, ok := byName[name]
		if !ok {
			t.Fatalf("missing case %s", name)
		}
		got := Classify(rule.Input)
		if !reflect.DeepEqual(got, rule.Result) {
			t.Fatalf("%s = %#v\nwant %#v", name, got, rule.Result)
		}
		switch got.Class {
		case ClassAgent:
			if name != "k8s_agent_exit_10" {
				t.Fatalf("%s class %s", name, got.Class)
			}
		case ClassInfrastructure, ClassConfiguration:
		default:
			t.Fatalf("%s class %s", name, got.Class)
		}
		if got.Class == ClassInfrastructure && got.Code == CodeUnknown && got.Summary != rule.Input.FailureReason {
			t.Fatalf("%s summary %q reason %q", name, got.Summary, rule.Input.FailureReason)
		}
	}
}
