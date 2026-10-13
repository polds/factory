package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	adapterName = "generic-openai"
	maxTurns    = 8
)

const systemPrompt = "You are a coding agent. The working directory is the workspace. Use the tools to complete the task. When you are finished, reply with a short summary and no tool calls. If you cannot complete the task, reply with only this JSON object and no other text: {\"status\":\"failed_by_agent\",\"summary\":\"what failed\"}."

// Options is the process boundary for one harness invocation.
type Options struct {
	Args       []string
	Env        []string
	Workspace  string
	RuntimeDir string
	Stderr     io.Writer
	Now        func() time.Time
}

// Run executes one harness invocation and returns a contract exit code.
func Run(ctx context.Context, opt Options) int {
	if opt.Stderr == nil {
		opt.Stderr = io.Discard
	}
	if opt.Workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			warnf(opt.Stderr, "factory-harness: %v\n", err)
			return ExitEnvironment
		}
		opt.Workspace = wd
	}
	if opt.RuntimeDir == "" {
		if dir := lookupEnv(opt.Env, "FACTORY_RUNTIME_DIR"); dir != "" {
			opt.RuntimeDir = dir
		} else {
			opt.RuntimeDir = "/var/run/factory"
		}
	}

	inv := ParseArgs(opt.Args)
	for _, w := range inv.Warnings {
		warnf(opt.Stderr, "factory-harness: %s\n", w)
	}

	if err := os.MkdirAll(opt.RuntimeDir, 0o700); err != nil {
		warnf(opt.Stderr, "factory-harness: %v\n", err)
		return ExitEnvironment
	}
	ef, err := os.OpenFile(filepath.Join(opt.RuntimeDir, "events.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		warnf(opt.Stderr, "factory-harness: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = ef.Close() }()
	log := NewLog(ef, opt.Now)

	var secrets []string
	finish := func(code int, status, summary string) int {
		summary = Redact(summary, secrets)
		_ = log.Emit(Event{Type: "terminal", Status: status, Summary: summary})
		warnf(opt.Stderr, "factory-harness: %s\n", summary)
		return code
	}

	if inv.TaskID() == "" {
		return finish(ExitConfigInvalid, "config_invalid", "missing --task-id")
	}

	task, err := LoadTaskFile(filepath.Join(opt.RuntimeDir, "task.json"))
	if err != nil {
		return finish(ExitConfigInvalid, "config_invalid", "task.json: "+err.Error())
	}

	name := inv.HarnessName()
	if name == "" {
		name = adapterName
	}
	if name != adapterName {
		return finish(ExitConfigInvalid, "config_invalid", fmt.Sprintf("harness adapter %q is not in this build", name))
	}

	model := inv.Model()
	if model == "" {
		model = task.ModelID
	}
	if model == "" {
		return finish(ExitConfigInvalid, "config_invalid", "model is required")
	}
	baseURL := lookupEnv(opt.Env, "OPENAI_BASE_URL")
	if baseURL == "" {
		return finish(ExitConfigInvalid, "config_invalid", "OPENAI_BASE_URL is not set")
	}

	secrets = secretList(task, opt.Env)

	if task.SetupCommand != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", task.SetupCommand)
		cmd.Dir = opt.Workspace
		cmd.Env = scrubEnv(opt.Env, task.HarnessAuthEnv)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return finish(ExitEnvironment, "environment_setup_failed", "setup command failed: "+msg)
		}
	}

	apiKey := lookupEnv(opt.Env, "OPENAI_API_KEY")
	box := toolbox{workspace: opt.Workspace, env: scrubEnv(opt.Env, task.HarnessAuthEnv)}

	summary := "skipped initial turn"
	if !inv.SkipInitialTurn() {
		prompt := task.Prompt
		if prompt == "" {
			prompt = lookupEnv(opt.Env, "FACTORY_TASK_PROMPT")
		}
		if prompt == "" {
			return finish(ExitConfigInvalid, "config_invalid", "prompt is empty")
		}
		code, status, summaryRun := runConversation(ctx, log, box, baseURL, apiKey, model, prompt, secrets)
		summary = summaryRun
		if code != ExitOK {
			return finish(code, status, summary)
		}
	}

	idle, err := idleDuration(inv.IdleOnComplete())
	if err != nil {
		return finish(ExitConfigInvalid, "config_invalid", err.Error())
	}
	deadline := time.Now().Add(idle)
	for idle > 0 && time.Now().Before(deadline) {
		next, ok := pollFollowup(ctx, opt.RuntimeDir, deadline)
		if !ok {
			break
		}
		code, status, summaryRun := runConversation(ctx, log, box, baseURL, apiKey, model, next, secrets)
		summary = summaryRun
		if code != ExitOK {
			return finish(code, status, summary)
		}
		deadline = time.Now().Add(idle)
	}
	return finish(ExitOK, "succeeded", summary)
}

func idleDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0s" || raw == "0m" || raw == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid --idle-on-complete %q", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --idle-on-complete %q", raw)
	}
	return d, nil
}

func pollFollowup(ctx context.Context, runtime string, deadline time.Time) (string, bool) {
	path := filepath.Join(runtime, "followup.json")
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return "", false
		}
		b, err := os.ReadFile(path)
		if err == nil {
			var body struct {
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal(b, &body) == nil && strings.TrimSpace(body.Prompt) != "" {
				_ = os.Remove(path)
				return body.Prompt, true
			}
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", false
		case <-timer.C:
		}
	}
}

func runConversation(ctx context.Context, log *Log, box toolbox, baseURL, apiKey, model, prompt string, secrets []string) (int, string, string) {
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}
	for turn := 1; turn <= maxTurns; turn++ {
		if err := log.Emit(Event{Type: "turn.started", Turn: turn, ModelID: model}); err != nil {
			return ExitEnvironment, "environment_setup_failed", err.Error()
		}
		resp, err := completeChat(ctx, baseURL, apiKey, chatRequest{
			Model:    model,
			Messages: messages,
			Tools:    toolSchema,
		})
		if err != nil {
			code := classifyProvider(err)
			status := "model_unavailable"
			if code == ExitConfigInvalid {
				status = "config_invalid"
			}
			return code, status, err.Error()
		}
		msg := resp.Choices[0].Message
		if resp.Usage != nil {
			_ = log.Emit(Event{
				Type:         "usage",
				Turn:         turn,
				ModelID:      model,
				InputTokens:  resp.Usage.PromptTokens,
				OutputTokens: resp.Usage.CompletionTokens,
			})
		}
		if text := strings.TrimSpace(msg.Content); text != "" && len(msg.ToolCalls) == 0 {
			red := Redact(text, secrets)
			_ = log.Emit(Event{Type: "message.delta", Turn: turn, Role: "assistant", Text: red})
			if summary, ok := agentFailure(text); ok {
				return ExitAgentReported, "failed_by_agent", Redact(summary, secrets)
			}
			return ExitOK, "succeeded", red
		}
		if len(msg.ToolCalls) == 0 {
			return ExitModelUnavailable, "model_unavailable", "model returned an empty message"
		}
		messages = append(messages, chatMessage{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})
		for _, call := range msg.ToolCalls {
			rawIn := visibleInput(call.Function.Name, call.Function.Arguments)
			_ = log.Emit(Event{
				Type:          "tool.call",
				Turn:          turn,
				Tool:          call.Function.Name,
				RedactedInput: Redact(rawIn, secrets),
				InputDigest:   Digest(rawIn),
			})
			start := time.Now()
			result := box.run(ctx, call.Function.Name, call.Function.Arguments)
			ms := time.Since(start).Milliseconds()
			ok := result.OK
			_ = log.Emit(Event{
				Type:         "tool.result",
				Turn:         turn,
				Tool:         call.Function.Name,
				OK:           &ok,
				DurationMS:   &ms,
				OutputDigest: Digest(result.Output),
			})
			if result.Changed {
				_ = log.Emit(Event{
					Type:      "file.changed",
					Path:      result.RelPath,
					Additions: result.Additions,
					Deletions: result.Deletions,
				})
			}
			messages = append(messages, chatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    result.Output,
			})
		}
	}
	return ExitAgentReported, "failed_by_agent", fmt.Sprintf("stopped after %d model turns", maxTurns)
}

func visibleInput(name, args string) string {
	if name == "bash" {
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(args), &p) == nil && p.Command != "" {
			return p.Command
		}
	}
	return args
}

// agentFailure recognizes the only successful-process failure the contract
// separates from infrastructure: the model reports that its own work failed.
// The JSON object must be the entire message so ordinary prose cannot trip it.
func agentFailure(content string) (string, bool) {
	var body struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &body) != nil {
		return "", false
	}
	if body.Status != "failed_by_agent" || body.Summary == "" {
		return "", false
	}
	return body.Summary, true
}

// warnf reports a diagnostic. A failed write must not change the contract exit code.
func warnf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
