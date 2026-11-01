package server

import (
	"strings"
	"testing"
)

func TestKubernetesSnapshotOmitsPlantedSecret(t *testing.T) {
	const secret = "sk-planted-k8s-9f3c1a"
	raw := []byte(`{
		"message": "` + secret + `",
		"kubernetes": {
			"schema_version": 1,
			"observation_source": "pod_watch",
			"observed_at": "2026-11-01T15:04:05Z",
			"truncated": true,
			"pod": {
				"name": "` + secret + `",
				"uid": "` + secret + `",
				"phase": "Pending",
				"reason": "Unschedulable",
				"message": "` + secret + `",
				"conditions": [{"type": "PodScheduled", "status": "False", "reason": "Unschedulable"}]
			},
			"job": {"name": "` + secret + `", "uid": "` + secret + `"},
			"container": {
				"name": "` + secret + `",
				"state": "waiting",
				"waiting_reason": "ImagePullBackOff",
				"message": "` + secret + `"
			},
			"events": [{"reason": "Failed", "message": "` + secret + `"}]
		}
	}`)
	got := whitelistSnapshot(raw)
	if got["pod.phase"] != "Pending" || got["pod.reason"] != "Unschedulable" {
		t.Fatalf("snapshot %#v", got)
	}
	if got["container.state"] != "waiting" || got["container.waiting_reason"] != "ImagePullBackOff" {
		t.Fatalf("container %#v", got)
	}
	if got["events"] != "Failed" || got["truncated"] != "true" || got["observed_at"] == "" {
		t.Fatalf("snapshot %#v", got)
	}
	for key, val := range got {
		if strings.Contains(key, secret) || strings.Contains(val, secret) {
			t.Fatalf("snapshot leaked %s=%s", key, val)
		}
	}
}
