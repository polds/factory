package server

import (
	"testing"

	"github.com/polds/factory/internal/failure"
)

// TestRequestRejectedWithoutExitNeverRetries calls reviewerInfra, the
// production decision retryReviewerOnce and recoverReviewerRetries use.
func TestRequestRejectedWithoutExitNeverRetries(t *testing.T) {
	t.Parallel()
	exit20 := 20
	cases := []failure.Input{
		{
			RunStatus:     failure.StatusFailed,
			HarnessStatus: failure.ReasonRequestRejected,
		},
		{
			RunStatus:     failure.StatusFailed,
			HarnessStatus: failure.ReasonRequestRejected,
			ExitCode:      &exit20,
		},
		{
			RunStatus:     failure.StatusFailed,
			FailureReason: failure.ReasonContainerExit,
			ExitCode:      &exit20,
			HarnessStatus: failure.ReasonRequestRejected,
		},
		{
			RunStatus:     failure.StatusFailed,
			FailureReason: failure.ReasonRequestRejected,
		},
	}
	for _, in := range cases {
		got := failure.Classify(in)
		if reviewerInfra(in) {
			t.Fatalf("reviewerInfra retried %#v class %s/%s", in, got.Class, got.Code)
		}
		if got.Class != failure.ClassConfiguration || got.Code != failure.CodeConfigInvalid || got.Retryable {
			t.Fatalf("Classify %#v = %#v", in, got)
		}
		if in.HarnessStatus == failure.ReasonRequestRejected {
			found := false
			for _, line := range got.Evidence {
				if line == "harness status request_rejected" {
					found = true
				}
			}
			if !found {
				t.Fatalf("evidence missing harness status: %#v", got.Evidence)
			}
		}
	}

	exit10 := 10
	agent := failure.Input{
		RunStatus:     failure.StatusFailed,
		FailureReason: failure.ReasonContainerExit,
		ExitCode:      &exit10,
		HarnessStatus: failure.ReasonRequestRejected,
	}
	if reviewerInfra(agent) {
		t.Fatal("exit 10 with request_rejected retried")
	}
	if got := failure.Classify(agent); got.Class != failure.ClassAgent {
		t.Fatalf("exit 10 class %s", got.Class)
	}

	exit21 := 21
	if !reviewerInfra(failure.Input{RunStatus: failure.StatusFailed, ExitCode: &exit21, FailureReason: failure.ReasonModelUnavailable}) {
		t.Fatal("model_unavailable did not stay retryable")
	}
}
