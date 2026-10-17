package failure

import "testing"

func FuzzClassify(f *testing.F) {
	f.Add("failed", "container_exit", 10, true, "failed_by_agent", []byte(nil))
	f.Add("failed", "container_oom", 137, true, "", []byte(mustFixture("testdata/oom.json")))
	f.Add("failed", "evicted", 143, true, "", []byte(mustFixture("testdata/evicted.json")))
	f.Add("cancelled", "", 0, false, "", []byte(`{"message":"secret"}`))
	f.Add("", "", 0, false, "", []byte("{"))
	f.Add("orphaned", "lease_expired", -1, true, "nope", []byte(`{"kubernetes":{"pod":{"reason":"Evicted","message":"x"}}}`))
	f.Fuzz(func(t *testing.T, status, reason string, exit int, hasExit bool, harness string, details []byte) {
		var code *int
		if hasExit {
			n := exit
			code = &n
		}
		got := Classify(Input{
			RunStatus:     status,
			FailureReason: reason,
			ExitCode:      code,
			HarnessStatus: harness,
			Details:       details,
		})
		if len(got.Evidence) > maxEvidence {
			t.Fatalf("evidence %d", len(got.Evidence))
		}
		switch got.Class {
		case ClassAgent, ClassInfrastructure, ClassConfiguration, ClassCancelled, "":
		default:
			t.Fatalf("class %q", got.Class)
		}
		if (got.Class == "") != (got.Code == "" && got.Summary == "" && !got.Retryable && len(got.Evidence) == 0) {
			t.Fatalf("empty class with payload %#v", got)
		}
	})
}
