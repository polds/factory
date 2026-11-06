package invocation_test

import (
	"errors"
	"testing"

	"github.com/polds/factory/internal/invocation"
)

func TestINVReplayCanonicalDigest(t *testing.T) {
	req := invocation.Request{
		DefinitionRevision:  "rev-applied-1",
		Profile:             invocation.Profile{Name: "scan"},
		Target:              &invocation.Target{Repository: "example/atum-core", PullRequest: 2213},
		Inputs:              map[string]any{"instruction": "review"},
		SourceOccurrenceKey: "human:review:1",
		Correlation:         &invocation.Correlation{ThreadRef: "slack:T/C/1555"},
		Intent:              "new",
		Priority:            "interactive",
	}
	const want = "b67e84668f3df343d6203b54a72377e2eb59ca7d919a9463efeab47c7b7111a0"
	if got := invocation.Digest(req); got != want {
		t.Fatalf("digest %q want %q", got, want)
	}
}

func TestINVProfileRejected(t *testing.T) {
	raw := map[string]any{
		"intent":              "new",
		"priority":            "interactive",
		"sourceOccurrenceKey": "k",
		"profile":             map[string]any{"name": "scan", "budgetCeilingCents": 2500},
	}
	err := invocation.ValidateRequest(raw)
	var rej invocation.Reject
	if err == nil || !errors.As(err, &rej) || rej.Code != invocation.CodeProfileRejected {
		t.Fatalf("err %v", err)
	}
}
