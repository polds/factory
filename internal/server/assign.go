package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/inference"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

const (
	defaultDockerImage  = "factory-m1-task:dev"
	defaultSidecarImage = "factory-harness:dev"
	defaultMaxTurns     = 40

	// Same identity the M2 live agent used once it configured git itself.
	defaultGitName  = "Agent"
	defaultGitEmail = "agent@example.com"
)

func (s *Server) taskEnv(ctx context.Context, run store.Run, bound boundAgent) (map[string]string, error) {
	factorySecrets := []string(nil)
	if tree := s.snapshot().tree; tree != nil {
		factorySecrets = tree.Factory.Secrets
	}
	agentSecrets := bound.agent.Secrets
	if auto, ok := s.intakeAutomation(ctx, run.ID); ok {
		agentSecrets = auto.Secrets
	}
	allow := secretNames(factorySecrets, agentSecrets)
	env := make(map[string]string, len(allow)+12)
	var values []string
	for _, name := range allow {
		value, ok := s.cfg.Secrets[name]
		if !ok || value == "" {
			return nil, fmt.Errorf("server: secret %s is not configured", name)
		}
		if name == "GH_TOKEN" && s.cfg.GitHubTokenMode == tokenModeApp {
			minted, err := s.mintGitHubToken(ctx, run)
			if err != nil {
				return nil, err
			}
			value = minted
		}
		env[name] = value
		values = append(values, value)
	}
	token, err := newRunToken()
	if err != nil {
		return nil, err
	}
	if run.Prompt != "" {
		env["FACTORY_TASK_PROMPT"] = run.Prompt
	}
	if run.WorkItemID != "" {
		env["FACTORY_WORK_ITEM_ID"] = run.WorkItemID
	}
	if run.ConversationID != "" {
		env["FACTORY_CONVERSATION_ID"] = run.ConversationID
	}
	if run.Stage != "" {
		env["FACTORY_STAGE"] = run.Stage
	}
	turns := bound.agent.MaxTurns
	if turns <= 0 {
		turns = defaultMaxTurns
	}
	env["FACTORY_MAX_TURNS"] = strconv.Itoa(turns)
	base := strings.TrimRight(s.cfg.TaskServerURL, "/")
	env["FACTORY_EVENTS_URL"] = base + "/api/v1/agent/runs/" + run.ID + "/events:ingest"
	env["FACTORY_RUN_TOKEN"] = token
	values = append(values, token)
	if s.cfg.InferenceBaseURL != "" {
		env["OPENAI_BASE_URL"] = s.cfg.InferenceBaseURL
	}
	if s.cfg.InferenceAPIKey != "" {
		env["OPENAI_API_KEY"] = s.cfg.InferenceAPIKey
		values = append(values, s.cfg.InferenceAPIKey)
	}
	// The upstream worker copies EnvVars into the task container. The
	// harness reads this variable on every model call, including review.
	if formatted := inference.Format(s.cfg.InferenceExtraHeaders); formatted != "" {
		env[inference.ExtraHeadersEnv] = formatted
		values = append(values, inference.Values(s.cfg.InferenceExtraHeaders)...)
	}
	env["FACTORY_SECRET_VALUES"] = strings.Join(values, "\n")
	// The task image has no git user (arbitrary uid, empty global config).
	// Git reads these instead of user.name, so the first commit can succeed.
	// An allowlisted secret of the same name is already in env and wins.
	setEnvDefault(env, "GIT_AUTHOR_NAME", defaultGitName)
	setEnvDefault(env, "GIT_AUTHOR_EMAIL", defaultGitEmail)
	setEnvDefault(env, "GIT_COMMITTER_NAME", defaultGitName)
	setEnvDefault(env, "GIT_COMMITTER_EMAIL", defaultGitEmail)
	return env, nil
}

func setEnvDefault(env map[string]string, key, value string) {
	if env[key] == "" {
		env[key] = value
	}
}

// assignment sets AgentConfigSnapshot.IdleTimeoutMinutes when the bound
// agent, or the intake automation, has a positive idle timeout. The worker
// turns that into --idle-on-complete. factoryd does not build the harness
// argv. A zero timeout leaves the field unset, and the worker's bare flag
// means the container exits when the conversation ends.
func (s *Server) assignment(ctx context.Context, run store.Run, exec store.Execution, bound boundAgent, env map[string]string) protocol.TaskAssignment {
	now := run.CreatedAt
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	model := run.ModelID
	if model == "" {
		model = bound.agent.Model
	}
	if model == "" {
		if snap := s.snapshot(); snap.tree != nil {
			model = snap.tree.Factory.AgentDefaults.Model
		}
	}
	prompt := run.Prompt
	title := run.Title
	if title == "" {
		title = bound.name
	}
	image := bound.runner.DockerImage
	if image == "" {
		image = defaultDockerImage
	}
	asg := protocol.TaskAssignment{
		TaskID:       run.ID,
		ExecutionID:  exec.ID,
		DockerImage:  image,
		SidecarImage: defaultSidecarImage,
		EnvVars:      env,
		Task: &protocol.Task{
			ID:    run.ID,
			Title: title,
			Definition: protocol.TaskDefinition{
				Prompt: prompt,
			},
			CreatedAt: now,
			UpdatedAt: now,
			AgentConfigSnapshot: &protocol.AgentConfigSnapshot{
				BasePrompt: &prompt,
				ModelID:    &model,
			},
		},
	}
	minutes := s.effectiveIdleMinutes(ctx, run, bound)
	if minutes > 0 {
		asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes = &minutes
	}
	if bound.runner.VCPUs > 0 || bound.runner.MemoryGB > 0 {
		asg.InstanceShape = &protocol.InstanceShape{Vcpus: bound.runner.VCPUs, MemoryGb: bound.runner.MemoryGB}
	}
	return asg
}

func newRunToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("server: run token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
