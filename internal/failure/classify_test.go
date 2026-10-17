package failure

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCases(t *testing.T) {
	seen := map[string]bool{}
	var sawEmpty, sawSignal9, sawSignal15, sawOOM, sawEvicted, sawOrphaned, sawSuperseded, sawCancelled, sawUnknown bool
	for _, tc := range Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			if seen[tc.Name] {
				t.Fatalf("duplicate case %s", tc.Name)
			}
			seen[tc.Name] = true
			got := Classify(tc.Input)
			if !reflect.DeepEqual(got, tc.Result) {
				t.Fatalf("Classify() = %#v\nwant %#v", got, tc.Result)
			}
			if len(got.Evidence) > maxEvidence {
				t.Fatalf("evidence has %d lines", len(got.Evidence))
			}
			if tc.Name == "empty" && (got.Class != "" || got.Code != "" || got.Summary != "" || got.Retryable || got.Evidence != nil) {
				t.Fatalf("empty result %#v", got)
			}
		})
		switch tc.Name {
		case "empty":
			sawEmpty = true
		case "signal_9":
			sawSignal9 = true
		case "signal_15":
			sawSignal15 = true
		case "oom_killed":
			sawOOM = true
		case "evicted":
			sawEvicted = true
		case "orphaned":
			sawOrphaned = true
		case "superseded":
			sawSuperseded = true
		case "cancelled":
			sawCancelled = true
		case "unknown_reason":
			sawUnknown = true
		}
	}
	if !sawEmpty || !sawSignal9 || !sawSignal15 || !sawOOM || !sawEvicted || !sawOrphaned || !sawSuperseded || !sawCancelled || !sawUnknown {
		t.Fatalf("table is missing a required row")
	}
}

func TestRetryableOnlyForModel(t *testing.T) {
	for _, tc := range Cases() {
		want := tc.Result.Code == CodeModelUnavailable
		if tc.Result.Retryable != want {
			t.Fatalf("%s retryable %v", tc.Name, tc.Result.Retryable)
		}
	}
}

func TestPlantedSecret(t *testing.T) {
	const secret = "sk-planted-7f3a9c"
	oom := mustFixture("testdata/oom.json")
	evicted := mustFixture("testdata/evicted.json")
	inputs := []Input{
		{
			RunStatus:     StatusFailed,
			FailureReason: ReasonContainerOOM,
			ExitCode:      codePtr(137),
			Details:       plant(t, oom, secret),
		},
		{
			RunStatus:     StatusFailed,
			FailureReason: ReasonEvicted,
			ExitCode:      codePtr(143),
			Details:       plant(t, evicted, secret),
		},
		{
			RunStatus: StatusFailed,
			ExitCode:  codePtr(10),
			Details:   plant(t, []byte(`{"message":"","log":""}`), secret),
		},
		{
			RunStatus:     StatusFailed,
			FailureReason: secret,
			ExitCode:      codePtr(10),
			Details:       plant(t, []byte(`{"kubernetes":{"pod":{"reason":"Evicted"}}}`), secret),
		},
	}
	for i, in := range inputs {
		got := Classify(in)
		blob := got.Summary + "\n" + strings.Join(got.Evidence, "\n")
		blob += "\n" + string(got.Class) + "\n" + got.Code
		for _, formatted := range []string{
			blob,
			got.Summary,
			strings.Join(got.Evidence, "\n"),
		} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("input %d leaked secret into %q", i, formatted)
			}
		}
		if got.Class == "" {
			t.Fatalf("input %d classified as not failed", i)
		}
	}
}

func plant(t *testing.T, raw []byte, secret string) json.RawMessage {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["message"] = secret
	doc["log"] = "pod log " + secret
	k, _ := doc["kubernetes"].(map[string]any)
	if k == nil {
		k = map[string]any{}
		doc["kubernetes"] = k
	}
	pod, _ := k["pod"].(map[string]any)
	if pod == nil {
		pod = map[string]any{}
		k["pod"] = pod
	}
	pod["message"] = secret
	pod["status_message"] = "Evicted: " + secret
	container, _ := k["container"].(map[string]any)
	if container == nil {
		container = map[string]any{}
		k["container"] = container
	}
	container["message"] = secret
	events, _ := k["events"].([]any)
	events = append(events, map[string]any{
		"type":    "Warning",
		"reason":  "BackOff",
		"message": "back-off " + secret,
		"count":   2,
	})
	k["events"] = events
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), secret) {
		t.Fatal("fixture did not contain the planted secret")
	}
	return out
}

func TestHarnessFreeTextIgnored(t *testing.T) {
	const secret = "sk-planted-7f3a9c"
	got := Classify(Input{RunStatus: StatusSucceeded, HarnessStatus: secret})
	if got.Class != "" || got.Code != "" || got.Summary != "" || got.Retryable || got.Evidence != nil {
		t.Fatalf("%#v", got)
	}
	failed := Classify(Input{RunStatus: StatusFailed, HarnessStatus: secret})
	if failed.Code != CodeUnknown || strings.Contains(failed.Summary, secret) || strings.Contains(strings.Join(failed.Evidence, "\n"), secret) {
		t.Fatalf("%#v", failed)
	}
}

func TestMalformedDetails(t *testing.T) {
	got := Classify(Input{
		RunStatus: StatusFailed,
		ExitCode:  codePtr(137),
		Details:   json.RawMessage("{"),
	})
	if got.Code != CodeContainerKilled || strings.Contains(got.Summary, "{") {
		t.Fatalf("%#v", got)
	}
}

func TestExit10IgnoresFreeTextReason(t *testing.T) {
	const secret = "sk-planted-7f3a9c"
	got := Classify(Input{
		RunStatus:     StatusFailed,
		FailureReason: "container exploded: " + secret,
		ExitCode:      codePtr(10),
	})
	if got.Code != CodeAgentReported {
		t.Fatalf("code %s", got.Code)
	}
	if strings.Contains(got.Summary, secret) || strings.Contains(strings.Join(got.Evidence, "\n"), secret) {
		t.Fatalf("summary %q evidence %#v", got.Summary, got.Evidence)
	}
}

func TestUnknownNamesRawReason(t *testing.T) {
	got := Classify(Input{RunStatus: StatusFailed, FailureReason: "disk_full\nplease"})
	if got.Code != CodeUnknown || got.Summary != "disk_full please" {
		t.Fatalf("%#v", got)
	}
	if len(got.Evidence) != 1 || got.Evidence[0] != "failure reason disk_full please" {
		t.Fatalf("evidence %#v", got.Evidence)
	}
}
