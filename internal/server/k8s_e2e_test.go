package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/scorer"
)

// TestE2EM7K8sFailure sends a worker failure that carries Kubernetes details.
// The run fails as infrastructure, run detail serves the whitelisted
// snapshot with the planted secret removed, and the run is not scorer-eligible.
func TestE2EM7K8sFailure(t *testing.T) {
	skipNoDB(t)
	const secret = "sk-planted-k8s-9f3c1a"
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	runID := env.startRun(t, "k8s image pull")
	asg := w.readAssignment(t)
	observed := time.Date(2026, 11, 1, 15, 4, 5, 0, time.UTC)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID:        runID,
		ExecutionID:   asg.ExecutionID,
		Message:       "image pull " + secret,
		FailureReason: "image_pull",
		FailureDetails: &protocol.FailureDetails{
			Kubernetes: &protocol.KubernetesFailureDetails{
				SchemaVersion:     1,
				ObservationSource: "pod_watch",
				ObservedAt:        observed,
				Truncated:         true,
				Job: &protocol.KubernetesJobFailureDetails{
					Name: secret,
					UID:  secret,
				},
				Pod: &protocol.KubernetesPodFailureDetails{
					Name:   secret,
					UID:    secret,
					Phase:  "Pending",
					Reason: "Unschedulable",
				},
				Container: &protocol.KubernetesContainerFailureDetails{
					Kind:          "regular",
					Name:          secret,
					State:         "waiting",
					WaitingReason: "ImagePullBackOff",
				},
				Events: []protocol.KubernetesEventDetails{{
					Type:   "Warning",
					Reason: "Failed",
					Count:  2,
				}},
			},
		},
	})
	env.waitStatus(t, runID, "failed")

	code, raw := env.authed(t, http.MethodGet, "/api/v1/agent/runs/"+runID, "")
	if code != http.StatusOK {
		t.Fatalf("detail %d %s", code, raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("run detail contains the planted secret")
	}
	detail := env.runDetail(t, runID)
	if detail.Failure.Class != string(failure.ClassInfrastructure) || detail.Failure.Code != failure.CodeUnknown {
		t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
	if detail.FailureReason != "image_pull" || detail.Failure.Summary != "image_pull" {
		t.Fatalf("reason %s summary %s", detail.FailureReason, detail.Failure.Summary)
	}
	snap := detail.KubernetesSnapshot
	if snap["pod.phase"] != "Pending" || snap["pod.reason"] != "Unschedulable" {
		t.Fatalf("pod snapshot %#v", snap)
	}
	if snap["container.state"] != "waiting" || snap["container.waiting_reason"] != "ImagePullBackOff" {
		t.Fatalf("container snapshot %#v", snap)
	}
	if snap["events"] != "Failed" || snap["truncated"] != "true" || snap["observed_at"] == "" {
		t.Fatalf("snapshot %#v", snap)
	}
	if _, ok := snap["pod.name"]; ok {
		t.Fatalf("snapshot kept a name %#v", snap)
	}
	assertEligible(t, env, runID, detail, false)
}

// TestE2EM7Drain is the factoryd side of a worker drain. The in-flight task
// reports task_completed and is succeeded. The worker does not claim the
// task offered after it starts rejecting work, and that run is not an
// infrastructure failure.
//
// The 600s SIGTERM wait lives in the upstream worker, not in this process.
// At 63dbb8a the Kubernetes backend preserves Jobs and does not wait for
// them (worker.go shutdown, kubernetes.go PreservesTasksOnShutdown). That
// gap is a D5 finding. Upstream covers the behavior it does implement:
// TestHandleTaskAssignmentRejectsAfterShutdownStarts,
// TestWorkerShutdownPreservesActiveTasksForPreservingBackend, and
// TestKubernetesBackendShutdownPreservesWorkerJobs.
func TestE2EM7Drain(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{maxConcurrent: 2})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	inflight := env.startRun(t, "drain in flight")
	first := w.readAssignment(t)
	if first.TaskID != inflight || first.ExecutionID == "" {
		t.Fatalf("assignment %+v", first.TaskID)
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: inflight, WorkerID: "local-1"})

	next := env.startRun(t, "drain must not claim")
	offered := w.readAssignment(t)
	if offered.TaskID != next {
		t.Fatalf("offered %s", offered.TaskID)
	}
	w.send(t, protocol.TypeTaskRejected, protocol.TaskRejected{
		TaskID: next,
		Reason: "worker is shutting down",
	})

	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID:      inflight,
		ExecutionID: first.ExecutionID,
		Message:     "done",
	})
	env.waitStatus(t, inflight, "succeeded")
	done := env.runDetail(t, inflight)
	if done.Failure.Class != "" || done.Failure.Code != "" {
		t.Fatalf("in-flight class %s/%s", done.Failure.Class, done.Failure.Code)
	}
	assertEligible(t, env, inflight, done, true)

	again := w.readAssignment(t)
	if again.TaskID == inflight {
		t.Fatal("completed task was offered again")
	}
	if again.TaskID != next {
		t.Fatalf("reoffered %s", again.TaskID)
	}
	w.send(t, protocol.TypeTaskRejected, protocol.TaskRejected{
		TaskID: again.TaskID,
		Reason: "worker is shutting down",
	})

	status := env.status(t, next)
	if status == "failed" || status == "succeeded" || status == "cancelled" {
		t.Fatalf("new task status %s", status)
	}
	nextDetail := env.runDetail(t, next)
	if nextDetail.Failure.Class != "" {
		t.Fatalf("new task class %s", nextDetail.Failure.Class)
	}
	for _, state := range env.execStates(t, next) {
		if state == "failed" {
			t.Fatalf("reject wrote a failure: %v", env.execStates(t, next))
		}
	}
}

func assertEligible(t *testing.T, env *env, runID string, detail api.RunDetail, want bool) {
	t.Helper()
	var purpose string
	if err := env.raw.QueryRow(context.Background(), `SELECT purpose FROM runs WHERE id = $1`, runID).Scan(&purpose); err != nil {
		t.Fatal(err)
	}
	ok, why := scorer.Eligible(definition.Scorer{
		Name: "review",
		Agents: []definition.ScorerAgent{{
			Name: detail.Agent,
		}},
	}, scorer.RunFacts{
		RunID:        runID,
		Agent:        detail.Agent,
		Status:       detail.Status,
		FailureClass: detail.Failure.Class,
		Purpose:      purpose,
		Terminal:     detail.Status == failure.StatusSucceeded || detail.Status == failure.StatusFailed || detail.Status == failure.StatusCancelled,
	})
	if ok != want {
		t.Fatalf("eligible %v (%s)", ok, why)
	}
	if want {
		return
	}
	if !strings.Contains(why, "not eligible") {
		t.Fatalf("rejection %q", why)
	}
}
