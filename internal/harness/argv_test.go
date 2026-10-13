package harness

import "testing"

func TestParseWorkerArgv(t *testing.T) {
	in := ParseArgs([]string{
		"agent", "run",
		"--task-id", "run_m0",
		"--sandboxed",
		"--server-root-url", "http://127.0.0.1:8080",
		"--model", "m0-stub-model",
		"--computer-use",
		"--idle-on-complete",
	})
	if in.TaskID() != "run_m0" || in.Model() != "m0-stub-model" {
		t.Fatalf("task=%q model=%q", in.TaskID(), in.Model())
	}
	if !in.Bools["sandboxed"] || !in.Bools["computer-use"] {
		t.Fatalf("bools: %#v", in.Bools)
	}
	if in.IdleOnComplete() != "" {
		t.Fatalf("idle %q", in.IdleOnComplete())
	}
	if len(in.Warnings) != 2 || in.Warnings[0] != "ignoring positional argument agent" || in.Warnings[1] != "ignoring positional argument run" {
		t.Fatalf("warnings: %#v", in.Warnings)
	}
}

func TestParseBareIdleDoesNotSwallowNextFlag(t *testing.T) {
	in := ParseArgs([]string{"--idle-on-complete", "--sandboxed", "--model", ""})
	if in.IdleOnComplete() != "" {
		t.Fatalf("idle %q", in.IdleOnComplete())
	}
	if !in.Bools["sandboxed"] {
		t.Fatalf("sandboxed not set: %#v", in.Bools)
	}
	if in.Model() != "" {
		t.Fatalf("model %q", in.Model())
	}
}

func TestParseUnknownFlag(t *testing.T) {
	in := ParseArgs([]string{"--not-a-real-flag", "bogus", "--task-id", "t"})
	if in.TaskID() != "t" {
		t.Fatalf("task %q", in.TaskID())
	}
	if len(in.Warnings) != 1 || in.Warnings[0] != "ignoring unknown flag --not-a-real-flag bogus" {
		t.Fatalf("warnings %#v", in.Warnings)
	}
}
