package server

import (
	"context"
	"testing"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestScheduleBoundMeasurementFallback(t *testing.T) {
	s := &Server{cfg: Config{WorkerPool: "standard", WorkerPlatform: "linux"}}
	snap := loaded{tree: &definition.Tree{
		Factory: definition.Factory{ScorerDefaults: &definition.ScorerDefaults{Runner: "local"}},
		Runners: map[string]definition.Runner{
			"local": {Name: "local", DockerImage: "img", OS: "linux"},
		},
	}}
	bound, ok := s.scheduleBound(context.Background(), snap, store.Run{Purpose: api.PurposeScorer})
	if !ok {
		t.Fatal("judge run was not schedulable")
	}
	if bound.agent.Pool != "standard" || bound.runner.DockerImage != "img" || bound.name != "" {
		t.Fatalf("bound %+v", bound)
	}
	if _, ok := s.scheduleBound(context.Background(), snap, store.Run{Purpose: api.PurposeWork}); ok {
		t.Fatal("work run without an agent resolved")
	}
}

func TestM6TokenPermissions(t *testing.T) {
	task := improvementTaskPermissions()
	pr := improvementPRPermissions()
	wantTask := map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	wantPR := map[string]string{"pull_requests": "write", "issues": "write", "checks": "write", "metadata": "read"}
	if !samePerms(task, wantTask) || !samePerms(pr, wantPR) {
		t.Fatalf("task %v pr %v", task, pr)
	}
	if _, ok := task["administration"]; ok {
		t.Fatal("task token has administration")
	}
	if _, ok := pr["administration"]; ok {
		t.Fatal("pr token has administration")
	}
	if _, ok := pr["contents"]; ok {
		t.Fatal("pr token has contents")
	}
	if _, ok := task["issues"]; ok {
		t.Fatal("task token has issues")
	}
	task["contents"] = "read"
	if improvementTaskPermissions()["contents"] != "write" {
		t.Fatal("permission map is shared")
	}
}

func samePerms(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
