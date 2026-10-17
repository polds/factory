package failure

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Class is who failed the run. The empty class means the run did not fail.
type Class string

const (
	ClassAgent          Class = "agent"
	ClassInfrastructure Class = "infrastructure"
	ClassConfiguration  Class = "configuration"
	ClassCancelled      Class = "cancelled"
)

// Stable machine codes. The API and the console match on these, not on Summary.
const (
	CodeAgentReported          = "agent_reported"
	CodeConfigInvalid          = "config_invalid"
	CodeModelUnavailable       = "model_unavailable"
	CodeEnvironmentSetupFailed = "environment_setup_failed"
	CodeOOMKilled              = "oom_killed"
	CodeEvicted                = "evicted"
	CodeContainerKilled        = "container_killed"
	CodeContainerExit          = "container_exit"
	CodeWorkerLost             = "worker_lost"
	CodeSuperseded             = "superseded"
	CodeCancelled              = "cancelled"
	CodeUnknown                = "unknown"
)

// Reason and harness-status codes the table matches on.
const (
	ReasonContainerExit          = "container_exit"
	ReasonContainerOOM           = "container_oom"
	ReasonEvicted                = "evicted"
	ReasonConfigInvalid          = "config_invalid"
	ReasonModelUnavailable       = "model_unavailable"
	ReasonEnvironmentSetupFailed = "environment_setup_failed"
	ReasonFailedByAgent          = "failed_by_agent"
	ReasonOrphaned               = "orphaned"
	ReasonLeaseExpired           = "lease_expired"
	ReasonWorkerGone             = "worker_gone"
	ReasonSuperseded             = "superseded_duplicate_building_run"

	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
	StatusSucceeded = "succeeded"
)

// Input is everything Classify is allowed to see.
// Message text is not a field: it must not change the class or the copy.
type Input struct {
	RunStatus     string          // runs.status, or an execution state such as orphaned
	FailureReason string          // runs/executions.failure_reason
	ExitCode      *int            // normalized, 128+signal when the process was signalled
	HarnessStatus string          // status of the harness terminal event, when one was recorded
	Details       json.RawMessage // executions.failure_details_json
}

// Result is the failure record the API returns and the console renders.
type Result struct {
	Class     Class    `json:"class"`
	Code      string   `json:"code,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Retryable bool     `json:"retryable"`
	Evidence  []string `json:"evidence,omitempty"`
}

// Rule is one input/result pair in the classification table.
type Rule struct {
	Name   string
	Input  Input
	Result Result
}

// Classify reports the class of one run or execution.
// It never reads message text. Malformed Details are treated as absent.
func Classify(in Input) Result {
	reason := strings.TrimSpace(in.FailureReason)
	harness := strings.TrimSpace(in.HarnessStatus)
	status := strings.TrimSpace(in.RunStatus)
	snap := parseSnapshot(in.Details)
	exit := effectiveExit(in.ExitCode, snap)

	switch {
	case harness == ReasonFailedByAgent || exitIs(exit, 10):
		return done(ClassAgent, CodeAgentReported, withExit("Agent reported this run as failed", exit), false, facts(exit, reason, harness, snap))
	case exitIs(exit, 20) || reason == ReasonConfigInvalid || harness == ReasonConfigInvalid:
		return done(ClassConfiguration, CodeConfigInvalid, withExit("Configuration is invalid", exit), false, facts(exit, reason, harness, snap))
	case exitIs(exit, 21) || reason == ReasonModelUnavailable || harness == ReasonModelUnavailable:
		return done(ClassInfrastructure, CodeModelUnavailable, withExit("Model provider is unavailable", exit), true, facts(exit, reason, harness, snap))
	case exitIs(exit, 30) || reason == ReasonEnvironmentSetupFailed || harness == ReasonEnvironmentSetupFailed:
		return done(ClassInfrastructure, CodeEnvironmentSetupFailed, withExit("Environment setup failed", exit), false, facts(exit, reason, harness, snap))
	case snap.oom() || reason == ReasonContainerOOM:
		return done(ClassInfrastructure, CodeOOMKilled, withExit("Container was OOMKilled", exit), false, facts(exit, reason, harness, snap))
	case snap.evicted() || reason == ReasonEvicted:
		return done(ClassInfrastructure, CodeEvicted, withExit("Pod was Evicted", exit), false, facts(exit, reason, harness, snap))
	case exit != nil && *exit >= 128:
		return done(ClassInfrastructure, CodeContainerKilled, killSummary(*exit), false, facts(exit, reason, harness, snap))
	case reason == ReasonContainerExit || (exit != nil && *exit > 0 && *exit < 128 && reason == ""):
		return done(ClassInfrastructure, CodeContainerExit, withExit("Container exited", exit), false, facts(exit, reason, harness, snap))
	case workerLostCode(status, reason) != "":
		code := workerLostCode(status, reason)
		ev := facts(exit, reason, harness, snap)
		if !hasLine(ev, "failure reason "+code) {
			ev = appendFact(ev, "run status "+code)
		}
		return done(ClassInfrastructure, CodeWorkerLost, "Worker lost the run ("+code+")", false, ev)
	case reason == ReasonSuperseded:
		return done(ClassCancelled, CodeSuperseded, "Run was superseded by a newer building run", false, facts(exit, reason, harness, snap))
	case status == StatusCancelled:
		ev := facts(exit, reason, harness, snap)
		ev = appendFact(ev, "run status "+StatusCancelled)
		return done(ClassCancelled, CodeCancelled, "Run was cancelled", false, ev)
	case reason != "":
		ev := facts(exit, reason, harness, snap)
		line := "failure reason " + oneLine(reason)
		if !hasLine(ev, line) {
			ev = appendFact(ev, line)
		}
		return done(ClassInfrastructure, CodeUnknown, oneLine(reason), false, ev)
	case status == StatusFailed:
		return done(ClassInfrastructure, CodeUnknown, StatusFailed, false, []string{"run status " + StatusFailed})
	default:
		return Result{}
	}
}

func effectiveExit(code *int, snap snapshot) *int {
	if code != nil {
		return code
	}
	if snap.normalized != nil {
		n := *snap.normalized
		return &n
	}
	if snap.signal > 0 {
		n := 128 + snap.signal
		return &n
	}
	return nil
}

func exitIs(code *int, want int) bool {
	return code != nil && *code == want
}

func workerLostCode(status, reason string) string {
	for _, c := range []string{reason, status} {
		switch c {
		case ReasonOrphaned, ReasonLeaseExpired, ReasonWorkerGone:
			return c
		}
	}
	return ""
}

func withExit(base string, exit *int) string {
	if exit == nil {
		return base
	}
	return fmt.Sprintf("%s (exit %d)", base, *exit)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func done(class Class, code, summary string, retryable bool, ev []string) Result {
	if len(ev) == 0 {
		ev = nil
	}
	return Result{
		Class:     class,
		Code:      code,
		Summary:   summary,
		Retryable: retryable,
		Evidence:  ev,
	}
}

// echoedReasons are codes Evidence may quote. Anything else is free text
// and is copied only by the unknown-reason row, which names it on purpose.
var echoedReasons = map[string]bool{
	ReasonContainerExit:          true,
	ReasonContainerOOM:           true,
	ReasonEvicted:                true,
	ReasonConfigInvalid:          true,
	ReasonModelUnavailable:       true,
	ReasonEnvironmentSetupFailed: true,
	ReasonFailedByAgent:          true,
	ReasonOrphaned:               true,
	ReasonLeaseExpired:           true,
	ReasonWorkerGone:             true,
	ReasonSuperseded:             true,
	"task_timeout":               true,
	"task_cancelled":             true,
	"graceful_shutdown":          true,
	"image_pull":                 true,
	"sidecar_prep":               true,
	"container_create":           true,
	"container_start":            true,
	"container_wait":             true,
	"workspace_setup":            true,
	"setup_command":              true,
	"agent_invocation":           true,
	"teardown_command":           true,
	"dispatch_command":           true,
	"dispatch_timeout":           true,
	"cancel_command":             true,
	"job_create":                 true,
	"job_watch":                  true,
	"job_failed":                 true,
	"pod_watch":                  true,
	"unschedulable":              true,
	"volume_mount":               true,
	"init_container":             true,
	"invalid_image":              true,
	"active_deadline":            true,
	"cleanup":                    true,
	"unknown":                    true,
	"cancelled":                  true,
}

var echoedHarness = map[string]bool{
	ReasonFailedByAgent:          true,
	ReasonConfigInvalid:          true,
	ReasonModelUnavailable:       true,
	ReasonEnvironmentSetupFailed: true,
}

// snapshotTokens are kubernetes reason and phase values Evidence may quote.
var snapshotTokens = map[string]bool{
	"OOMKilled":  true,
	"OOMKilling": true,
	"Evicted":    true,
	"Killing":    true,
	"Error":      true,
	"Completed":  true,
	"Failed":     true,
	"Pending":    true,
	"Running":    true,
	"Succeeded":  true,
	"Unknown":    true,
	"terminated": true,
	"waiting":    true,
	"running":    true,
}

func facts(exit *int, reason, harness string, snap snapshot) []string {
	var ev []string
	if exit != nil {
		ev = appendFact(ev, formatExit(*exit))
	}
	if echoedHarness[harness] {
		ev = appendFact(ev, "harness status "+harness)
	}
	if echoedReasons[reason] {
		ev = appendFact(ev, "failure reason "+reason)
	}
	if snapshotTokens[snap.termReason] {
		ev = appendFact(ev, "termination reason "+snap.termReason)
	}
	if snapshotTokens[snap.waitReason] {
		ev = appendFact(ev, "waiting reason "+snap.waitReason)
	}
	if snapshotTokens[snap.podReason] {
		ev = appendFact(ev, "pod reason "+snap.podReason)
	}
	if snapshotTokens[snap.podPhase] {
		ev = appendFact(ev, "pod phase "+snap.podPhase)
	}
	for _, r := range snap.eventReasons {
		if snapshotTokens[r] {
			ev = appendFact(ev, "event reason "+r)
		}
	}
	for _, r := range snap.podConditions {
		if snapshotTokens[r] {
			ev = appendFact(ev, "pod condition reason "+r)
		}
	}
	for _, r := range snap.jobConditions {
		if snapshotTokens[r] {
			ev = appendFact(ev, "job condition reason "+r)
		}
	}
	if snapshotTokens[snap.state] {
		ev = appendFact(ev, "container state "+snap.state)
	}
	return ev
}

const maxEvidence = 6

func appendFact(ev []string, line string) []string {
	if line == "" || len(ev) >= maxEvidence || hasLine(ev, line) {
		return ev
	}
	return append(ev, line)
}

func hasLine(ev []string, line string) bool {
	for _, got := range ev {
		if got == line {
			return true
		}
	}
	return false
}
