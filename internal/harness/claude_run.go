package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func routeAdapter(ctx context.Context, name string, opt Options, inv Invocation, log *Log, finish func(int, string, string) int) (int, bool) {
	switch name {
	case adapterName:
		return 0, false
	case claudeAdapterName:
		return runClaude(ctx, opt, inv, log, finish), true
	default:
		return finish(ExitConfigInvalid, "config_invalid", fmt.Sprintf("harness adapter %q is not in this build", name)), true
	}
}

func runClaude(ctx context.Context, opt Options, inv Invocation, log *Log, finish func(int, string, string) int) int {
	bin := lookupEnv(opt.Env, "FACTORY_CLAUDE_BIN")
	if bin == "" {
		return finish(ExitConfigInvalid, "config_invalid", "claude binary is not configured")
	}
	help, err := execClaude(ctx, bin, opt.Env, "--help")
	if err != nil {
		return finish(ExitConfigInvalid, "config_invalid", "claude help: "+err.Error())
	}
	caps, err := NegotiatePinned(help.Stdout)
	if err != nil {
		return finish(ExitConfigInvalid, "config_invalid", err.Error())
	}
	if !caps.StreamJSON || caps.NegotiatedSteer || !caps.WorkflowMainLoopOnly || caps.InternalWorkersAreRoster || !caps.InternalWorkersInSession {
		return finish(ExitConfigInvalid, "config_invalid", "claude capabilities are not the pinned set")
	}
	_ = log.Emit(Event{Type: "run.capabilities", Steer: caps.NegotiatedSteer})
	scenario := lookupEnv(opt.Env, "FACTORY_CLAUDE_SCENARIO")
	if scenario == "" {
		scenario = "empty-text"
	}
	args := []string{"--print", "--output-format", "stream-json", "--scenario", scenario}
	if resume := lookupEnv(opt.Env, "FACTORY_CLAUDE_RESUME"); resume != "" && caps.Resume {
		args = append(args, "--resume", resume)
	}
	out, err := execClaude(ctx, bin, opt.Env, args...)
	needles := secretList(TaskFile{}, opt.Env)
	if ctx.Err() != nil && strings.TrimSpace(out.Stdout) == "" {
		result := SessionResult{Attempts: []Attempt{{ID: "signal", Kind: "signal", Status: "signal"}}}
		return finishClaude(log, finish, result, needles, inv.TaskID())
	}
	result, mapErr := MapClaudeStream(strings.NewReader(out.Stdout))
	if errors.Is(mapErr, ErrMalformed) && !result.AccountedOnce() {
		return finish(ExitConfigInvalid, "config_invalid", "malformed claude stream")
	}
	if err != nil && ctx.Err() != nil {
		if len(result.Attempts) == 0 {
			result.Attempts = []Attempt{{ID: "signal", Kind: "signal", Status: "signal"}}
		}
	}
	return finishClaude(log, finish, result, needles, inv.TaskID())
}

func finishClaude(log *Log, finish func(int, string, string) int, result SessionResult, needles []string, taskID string) int {
	RedactAttempts(result.Attempts, needles)
	for _, attempt := range result.Attempts {
		emitAttempt(log, attempt)
	}
	code, status, summary := claudeExit(result)
	if taskID == "" {
		return finish(ExitConfigInvalid, "config_invalid", "missing --task-id")
	}
	if !result.AccountedOnce() {
		return finish(ExitConfigInvalid, "config_invalid", "claude attempts were not accounted once")
	}
	return finish(code, status, summary)
}

func emitAttempt(log *Log, attempt Attempt) {
	switch attempt.Kind {
	case "text", "refusal":
		_ = log.Emit(Event{Type: "message.delta", Role: "assistant", Text: attempt.Text, Status: attempt.Status})
	case "tool":
		ok := attempt.Status == "ok"
		_ = log.Emit(Event{Type: "tool.result", Tool: attempt.Tool, OK: &ok, OutputDigest: Digest(attempt.ToolOutput)})
	case "error", "signal":
		_ = log.Emit(Event{Type: "message.delta", Role: "assistant", Text: attempt.Text, Status: attempt.Status})
	case "usage", "resume":
		_ = log.Emit(Event{Type: "usage", InputTokens: attempt.InputTokens, OutputTokens: attempt.OutputTokens, Status: attempt.Status})
	default:
		_ = log.Emit(Event{Type: "message.delta", Status: attempt.Status, Text: attempt.Text})
	}
}

func claudeExit(result SessionResult) (int, string, string) {
	code, status := ExitOK, "ok"
	for _, attempt := range result.Attempts {
		nextCode, nextStatus := attemptExit(attempt)
		if nextCode > code {
			code, status = nextCode, nextStatus
		}
	}
	return code, status, "claude " + status
}

func attemptExit(attempt Attempt) (int, string) {
	switch attempt.Status {
	case "ok", "partial":
		if attempt.Status == "partial" {
			return ExitModelUnavailable, "partial"
		}
		return ExitOK, "ok"
	case "refusal", "tool_error":
		return ExitAgentReported, attempt.Status
	case "400", "malformed":
		return ExitConfigInvalid, attempt.Status
	case "401", "429", "timeout":
		return ExitModelUnavailable, attempt.Status
	case "signal":
		return ExitEnvironment, "signal"
	default:
		return ExitModelUnavailable, attempt.Status
	}
}

func cancelledSignal(ctx context.Context) (SessionResult, bool) {
	if ctx.Err() == nil {
		return SessionResult{}, false
	}
	return SessionResult{Attempts: []Attempt{{ID: "signal", Kind: "signal", Status: "signal"}}}, true
}

type claudeExec struct {
	Stdout string
	Stderr string
}

func execClaude(ctx context.Context, bin string, parent []string, args ...string) (claudeExec, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = isolateEnv(parent, secretList(TaskFile{}, parent))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return claudeExec{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

// RunClaudeOnce runs the pinned CLI once. A cancelled context with no
// stdout is one signal attempt.
func RunClaudeOnce(ctx context.Context, bin, scenario string) (SessionResult, error) {
	parent := []string{"PATH=" + os.Getenv("PATH")}
	if scenario == "hold" {
		_, execErr := execClaude(ctx, bin, parent, "--scenario", "hold")
		if result, ok := cancelledSignal(ctx); ok {
			return result, nil
		}
		return SessionResult{}, execErr
	}
	out, err := execClaude(ctx, bin, parent, "--print", "--output-format", "stream-json", "--scenario", scenario)
	result, mapErr := MapClaudeStream(strings.NewReader(out.Stdout))
	if mapErr != nil && !errors.Is(mapErr, ErrMalformed) {
		return result, mapErr
	}
	if err != nil && len(result.Attempts) == 0 {
		return result, err
	}
	return result, mapErr
}
