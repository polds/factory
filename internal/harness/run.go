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

const adapterName = "generic-openai"

const systemPrompt = "You are a coding agent. The working directory is the workspace. Use the tools to complete the task. When you are finished, reply with a short summary and no tool calls. If you cannot complete the task, reply with only this JSON object and no other text: {\"status\":\"failed_by_agent\",\"summary\":\"what failed\"}. When the task needs a decision from a person, call ask_input with the question and stop."

const (
	toolAskInput = "ask_input"
	questionGate = "clarification"
)

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
	eventOut := io.Writer(ef)
	rep := startEventReporter(opt.Env)
	if rep != nil {
		eventOut = reportWriter{w: ef, push: rep.push}
		defer rep.flush()
	}
	log := NewLog(eventOut, opt.Now)

	var (
		secrets    []string
		hub        *steerHub
		stopSteer  func()
		steerURL   string
		steerToken secretValue
	)
	finish := func(code int, status, summary string) int {
		// Stop the poller before the terminal event so a late capability
		// cannot land after it.
		if stopSteer != nil {
			stopSteer()
		}
		summary = Redact(summary, secrets)
		_ = log.Emit(Event{Type: "terminal", Status: status, Summary: summary})
		warnf(opt.Stderr, "factory-harness: %s\n", summary)
		return code
	}
	if rep != nil {
		if endpoint, err := steerEndpoint(lookupEnv(opt.Env, "FACTORY_EVENTS_URL")); err == nil {
			hub = newSteerHub()
			steerURL = endpoint
			steerToken = secretValue(lookupEnv(opt.Env, "FACTORY_RUN_TOKEN"))
			stopSteer = startSteerPoller(ctx, steerURL, steerToken, log, hub)
			defer stopSteer()
		}
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
	if tok := lookupEnv(opt.Env, "FACTORY_RUN_TOKEN"); tok != "" {
		secrets = append(secrets, tok)
	}

	turns, err := maxTurnsFrom(opt.Env)
	if err != nil {
		return finish(ExitConfigInvalid, "config_invalid", "FACTORY_MAX_TURNS: "+err.Error())
	}

	if task.SetupCommand != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", task.SetupCommand)
		cmd.Dir = opt.Workspace
		cmd.Env = scrubEnv(opt.Env, toolEnvDeny(task.HarnessAuthEnv))
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
	box := toolbox{workspace: opt.Workspace, env: scrubEnv(opt.Env, toolEnvDeny(task.HarnessAuthEnv))}
	// Parsed up front so an interrupt during the conversation can wait
	// out the same idle budget. A bad value is reported only after the
	// conversation, matching a run that never steers.
	idle, idleErr := idleDuration(inv.IdleOnComplete())
	if idleErr != nil {
		idle = 0
	}
	conv := conversation{
		log:      log,
		box:      box,
		baseURL:  baseURL,
		apiKey:   apiKey,
		model:    model,
		secrets:  secrets,
		maxTurns: turns,
		runtime:  opt.RuntimeDir,
		hub:      hub,
		idle:     idle,
	}

	summary := "skipped initial turn"
	if !inv.SkipInitialTurn() {
		prompt := task.Prompt
		if prompt == "" {
			prompt = lookupEnv(opt.Env, "FACTORY_TASK_PROMPT")
		}
		if prompt == "" {
			return finish(ExitConfigInvalid, "config_invalid", "prompt is empty")
		}
		code, status, summaryRun := conv.run(ctx, prompt)
		summary = summaryRun
		if code != ExitOK {
			return finish(code, status, summary)
		}
	}
	if idleErr != nil {
		return finish(ExitConfigInvalid, "config_invalid", idleErr.Error())
	}
	// A question already parked the run. Waiting out --idle-on-complete
	// would hold the worker while the answer requeues this same run.
	if conv.parked {
		return finish(ExitOK, "succeeded", summary)
	}

	applyFollowup := func(next followUp) (int, string) {
		if next.steerID != "" {
			_ = log.Emit(Event{
				Type:    "followup.received",
				SteerID: next.steerID,
				Text:    Redact(next.text, secrets),
			})
		}
		code, status, summaryRun := conv.run(ctx, next.text)
		summary = summaryRun
		return code, status
	}

	deadline := idleNow().Add(idle)
	for idle > 0 && idleNow().Before(deadline) {
		next, ok := pollFollowup(ctx, opt.RuntimeDir, deadline, hub)
		if !ok {
			break
		}
		code, status := applyFollowup(next)
		if code != ExitOK || conv.parked {
			return finish(code, status, summary)
		}
		deadline = idleNow().Add(idle)
	}
	// A follow-up already queued is a resume turn even when idle is 0.
	// That covers one that missed the end-of-turn drain and one left by
	// the last budgeted turn. Taking it is what acknowledges it.
	for {
		msg, ok := hub.takeFollowup()
		if !ok {
			break
		}
		code, status := applyFollowup(followUp{text: msg.Text, steerID: msg.ID})
		if code != ExitOK || conv.parked {
			return finish(code, status, summary)
		}
	}
	// Idle 0 does not wait. A follow-up whose long-poll is still in
	// flight was not in the hub, and finish would cancel that poll.
	// Stop the poller, then read once with wait=0. deliver ignores an id
	// the in-flight response already applied.
	if idle == 0 && hub != nil && steerURL != "" {
		if stopSteer != nil {
			stopSteer()
		}
		hub.collectImmediate(ctx, steerURL, steerToken, log)
		for {
			msg, ok := hub.takeFollowup()
			if !ok {
				break
			}
			code, status := applyFollowup(followUp{text: msg.Text, steerID: msg.ID})
			if code != ExitOK || conv.parked {
				return finish(code, status, summary)
			}
		}
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

// followUp is one resumed user turn. steerID is empty when it came from
// followup.json. Steer follow-ups and file follow-ups share this wait so
// the idle window has a single code path.
type followUp struct {
	text    string
	steerID string
}

func pollFollowup(ctx context.Context, runtime string, deadline time.Time, hub *steerHub) (followUp, bool) {
	path := filepath.Join(runtime, "followup.json")
	for {
		if text, ok := readFollowupFile(path); ok {
			return followUp{text: text}, true
		}
		if msg, ok := hub.takeFollowup(); ok {
			return followUp{text: msg.Text, steerID: msg.ID}, true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return followUp{}, false
		}
		var wake <-chan struct{}
		if hub != nil {
			wake = hub.wake
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return followUp{}, false
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func readFollowupFile(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(b, &body) != nil || strings.TrimSpace(body.Prompt) == "" {
		return "", false
	}
	_ = os.Remove(path)
	return body.Prompt, true
}

// turnCommitHook runs while the turn is still bound, after oneTurn has
// chosen its result and before unbindTurn. Tests deliver a steer into that
// window. Production leaves it nil.
var turnCommitHook func()

// followupMissedDrainHook runs after a successful turn's drain found
// nothing, before conversation.run returns. Tests deliver a follow-up that
// missed that drain. Production leaves it nil.
var followupMissedDrainHook func()

// idleNow is the clock for the post-conversation idle wait. Tests replace
// it so a positive --idle-on-complete does not sleep. Production uses time.Now.
var idleNow = time.Now

// conversation is one model session. A steer follow-up that arrives while
// the session is live is appended to messages. A follow-up that arrives
// after it has returned is delivered by pollFollowup and starts run again.
type conversation struct {
	log      *Log
	box      toolbox
	baseURL  string
	apiKey   string
	model    string
	secrets  []string
	maxTurns int
	runtime  string
	hub      *steerHub
	idle     time.Duration
	// parked is set when ask_input emits a question. The caller exits
	// without the idle-on-complete wait.
	parked bool
}

func (c *conversation) run(ctx context.Context, prompt string) (int, string, string) {
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}
	for turn := 1; turn <= c.maxTurns; turn++ {
		messages = c.drainFollowups(messages)
		var (
			cont    bool
			code    int
			status  string
			summary string
		)
		messages, cont, code, status, summary = c.oneTurn(ctx, turn, messages)
		if c.parked {
			return code, status, summary
		}
		if cont {
			continue
		}
		// A follow-up queued before this return extends the same conversation
		// when another turn is left in the budget. On the last budgeted
		// turn it stays queued so the caller runs it as its own
		// conversation, including when idle is 0.
		if code == ExitOK && turn < c.maxTurns {
			continued := c.drainFollowups(messages)
			if len(continued) > len(messages) {
				messages = continued
				continue
			}
		}
		if code == ExitOK && followupMissedDrainHook != nil {
			followupMissedDrainHook()
		}
		return code, status, summary
	}
	return ExitAgentReported, "failed_by_agent", fmt.Sprintf("stopped after %d model turns", c.maxTurns)
}

func (c *conversation) oneTurn(ctx context.Context, turn int, messages []chatMessage) ([]chatMessage, bool, int, string, string) {
	if err := c.log.Emit(Event{Type: "turn.started", Turn: turn, ModelID: c.model}); err != nil {
		return messages, false, ExitEnvironment, "environment_setup_failed", err.Error()
	}
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	var gen int
	if c.hub != nil {
		gen = c.hub.bindTurn(turnCancel)
		defer c.hub.unbindTurn(gen)
		if turnCommitHook != nil {
			defer turnCommitHook()
		}
	}
	resp, err := completeChat(turnCtx, c.baseURL, c.apiKey, chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    toolSchema,
	})
	if c.turnInterrupted(ctx, turnCtx) {
		return c.finishInterrupt(ctx, turn, gen, messages)
	}
	if err != nil {
		code := classifyProvider(err)
		status := "model_unavailable"
		if code == ExitConfigInvalid {
			status = "config_invalid"
		}
		return messages, false, code, status, err.Error()
	}
	msg := resp.Choices[0].Message
	if resp.Usage != nil {
		_ = c.log.Emit(Event{
			Type:         "usage",
			Turn:         turn,
			ModelID:      c.model,
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		})
	}
	if text := strings.TrimSpace(msg.Content); text != "" && len(msg.ToolCalls) == 0 {
		if c.turnInterrupted(ctx, turnCtx) {
			return c.finishInterrupt(ctx, turn, gen, messages)
		}
		red := Redact(text, c.secrets)
		_ = c.log.Emit(Event{Type: "message.delta", Turn: turn, Role: "assistant", Text: red})
		if summary, ok := agentFailure(text); ok {
			return messages, false, ExitAgentReported, "failed_by_agent", Redact(summary, c.secrets)
		}
		return messages, false, ExitOK, "succeeded", red
	}
	if len(msg.ToolCalls) == 0 {
		return messages, false, ExitModelUnavailable, "model_unavailable", "model returned an empty message"
	}
	if c.turnInterrupted(ctx, turnCtx) {
		return c.finishInterrupt(ctx, turn, gen, messages)
	}
	messages = append(messages, chatMessage{
		Role:      "assistant",
		Content:   msg.Content,
		ToolCalls: msg.ToolCalls,
	})
	for i, call := range msg.ToolCalls {
		if c.turnInterrupted(ctx, turnCtx) {
			messages = interruptedTools(messages, msg.ToolCalls[i:])
			return c.finishInterrupt(ctx, turn, gen, messages)
		}
		rawIn := visibleInput(call.Function.Name, call.Function.Arguments)
		redacted := Redact(rawIn, c.secrets)
		_ = c.log.Emit(Event{
			Type:          "tool.call",
			Turn:          turn,
			Tool:          call.Function.Name,
			RedactedInput: redacted,
			InputDigest:   Digest(rawIn),
		})
		start := time.Now()
		var result toolResult
		asked := false
		if call.Function.Name == toolAskInput {
			result, asked = askInputResult(call.Function.Arguments)
		} else {
			result = c.box.run(turnCtx, call.Function.Name, call.Function.Arguments)
		}
		ms := time.Since(start).Milliseconds()
		output := result.Output
		ok := result.OK
		if c.turnInterrupted(ctx, turnCtx) {
			ok = false
			if strings.TrimSpace(output) == "" {
				output = "interrupted"
			}
		}
		_ = c.log.Emit(Event{
			Type:         "tool.result",
			Turn:         turn,
			Tool:         call.Function.Name,
			OK:           &ok,
			DurationMS:   &ms,
			OutputDigest: Digest(output),
		})
		if result.Changed && !c.turnInterrupted(ctx, turnCtx) {
			_ = c.log.Emit(Event{
				Type:      "file.changed",
				Path:      result.RelPath,
				Additions: result.Additions,
				Deletions: result.Deletions,
			})
		}
		messages = append(messages, chatMessage{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    output,
		})
		if asked && !c.turnInterrupted(ctx, turnCtx) {
			// factoryd parks on this event and resumeIfParked starts the
			// next execution with the answer in the prompt. The idle
			// wait would hold this worker while that execution is queued.
			_ = c.log.Emit(Event{Type: "question", Turn: turn, Gate: questionGate, Text: redacted})
			c.parked = true
			return messages, false, ExitOK, "succeeded", "waiting for an answer"
		}
		if c.turnInterrupted(ctx, turnCtx) {
			messages = interruptedTools(messages, msg.ToolCalls[i+1:])
			return c.finishInterrupt(ctx, turn, gen, messages)
		}
	}
	return messages, true, 0, "", ""
}

func (c conversation) turnInterrupted(parent, turn context.Context) bool {
	return c.hub != nil && turn.Err() != nil && parent.Err() == nil
}

func (c conversation) finishInterrupt(ctx context.Context, turn, gen int, messages []chatMessage) ([]chatMessage, bool, int, string, string) {
	var queued []steerMsg
	for {
		msg, ok := c.hub.takeInterrupt()
		if !ok {
			break
		}
		queued = append(queued, msg)
	}
	// Drop the binding before the wait. An interrupt that arrives while
	// nothing is in flight is acknowledged and discarded, so the resumed
	// turn is not canceled by it.
	c.hub.releaseTurn(gen)
	for _, msg := range queued {
		_ = c.log.Emit(Event{
			Type:    "turn.interrupted",
			Turn:    turn,
			SteerID: msg.ID,
			Text:    Redact(msg.Text, c.secrets),
		})
	}
	if len(queued) == 0 {
		return messages, false, ExitModelUnavailable, "model_unavailable", "model call canceled"
	}
	// The idle deadline bounds the wait. A follow-up that is already
	// queued is taken before the deadline is consulted, so idle 0 still
	// resumes when the interrupt and the follow-up arrived together.
	next, ok := pollFollowup(ctx, c.runtime, time.Now().Add(c.idle), c.hub)
	if !ok {
		return messages, false, ExitOK, "succeeded", "turn interrupted"
	}
	if next.steerID != "" {
		_ = c.log.Emit(Event{
			Type:    "followup.received",
			SteerID: next.steerID,
			Text:    Redact(next.text, c.secrets),
		})
	}
	messages = append(messages, chatMessage{Role: "user", Content: next.text})
	return messages, true, 0, "", ""
}

func (c conversation) drainFollowups(messages []chatMessage) []chatMessage {
	if c.hub == nil {
		return messages
	}
	for {
		msg, ok := c.hub.takeFollowup()
		if !ok {
			return messages
		}
		_ = c.log.Emit(Event{
			Type:    "followup.received",
			SteerID: msg.ID,
			Text:    Redact(msg.Text, c.secrets),
		})
		messages = append(messages, chatMessage{Role: "user", Content: msg.Text})
	}
}

func interruptedTools(messages []chatMessage, calls []toolCall) []chatMessage {
	for _, call := range calls {
		messages = append(messages, chatMessage{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    "interrupted",
		})
	}
	return messages
}

func askInputResult(args string) (toolResult, bool) {
	if _, ok := askText(args); !ok {
		return toolResult{Output: "ask_input requires text"}, false
	}
	return toolResult{OK: true, Output: "asked"}, true
}

func askText(args string) (string, bool) {
	var p struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(args), &p) != nil {
		return "", false
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return "", false
	}
	return text, true
}

func visibleInput(name, args string) string {
	switch name {
	case "bash":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(args), &p) == nil && p.Command != "" {
			return p.Command
		}
	case toolAskInput:
		if text, ok := askText(args); ok {
			return text
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

func toolEnvDeny(extra []string) []string {
	out := make([]string, 0, len(extra)+2)
	out = append(out, extra...)
	out = append(out, "FACTORY_RUN_TOKEN", "FACTORY_EVENTS_URL")
	return out
}
