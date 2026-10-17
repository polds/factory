package server

import (
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestWithIdlePhaseInsertsGap(t *testing.T) {
	end := time.Date(2026, 4, 2, 12, 0, 2, 0, time.UTC)
	harnessAt := end.Add(-1500 * time.Millisecond)
	phases := []store.Phase{
		{Phase: store.PhaseRunning, At: end.Add(-4 * time.Second), ExecutionID: "exec_1"},
		{Phase: store.PhaseTerminal, At: end, ExecutionID: "exec_1"},
	}
	got := withIdlePhase(phases, harnessAt)
	if len(got) != 3 || got[1].Phase != api.PhaseIdle || !got[1].At.Equal(harnessAt) {
		t.Fatalf("phases %+v", got)
	}
	if got[1].DurationMS != 1500 || got[0].DurationMS != 2500 || got[2].DurationMS != 0 {
		t.Fatalf("durations %+v", got)
	}
}

func TestWithIdlePhaseSkipsWhenHarnessIsNotEarlier(t *testing.T) {
	end := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	phases := []store.Phase{{Phase: store.PhaseTerminal, At: end, ExecutionID: "exec_1"}}
	if got := withIdlePhase(phases, end); len(got) != 1 || got[0].Phase != store.PhaseTerminal {
		t.Fatalf("equal ts inserted idle: %+v", got)
	}
	if got := withIdlePhase(phases, time.Time{}); len(got) != 1 {
		t.Fatalf("missing harness ts inserted idle: %+v", got)
	}
}

func TestCleanUploadPath(t *testing.T) {
	ok := []string{"factory.yaml", "agents/implementer/agent.md"}
	for _, path := range ok {
		got, valid := cleanUploadPath(path)
		if !valid || got != path {
			t.Fatalf("path %q -> %q valid %v", path, got, valid)
		}
	}
	bad := []string{"", "/", "/etc/passwd", "../secret", "agents/../../etc/passwd", `agents\agent.md`, "dir/"}
	for _, path := range bad {
		if _, valid := cleanUploadPath(path); valid {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestConsoleRunURL(t *testing.T) {
	got := consoleRunURL("https://factory.example/", "run/1")
	if got != "https://factory.example/runs/run%2F1" {
		t.Fatalf("url %s", got)
	}
}
