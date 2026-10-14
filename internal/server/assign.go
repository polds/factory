package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

const (
	defaultDockerImage  = "factory-m1-task:dev"
	defaultSidecarImage = "factory-harness:dev"
	defaultMaxTurns     = 40
)

func (s *Server) taskEnv(run store.Run, bound boundAgent) (map[string]string, error) {
	allow := secretNames(s.snapshot().tree.Factory.Secrets, bound.agent.Secrets)
	env := make(map[string]string, len(allow)+8)
	var values []string
	for _, name := range allow {
		value, ok := s.cfg.Secrets[name]
		if !ok || value == "" {
			return nil, fmt.Errorf("server: secret %s is not configured", name)
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
	env["FACTORY_SECRET_VALUES"] = strings.Join(values, "\n")
	return env, nil
}

func (s *Server) assignment(run store.Run, exec store.Execution, bound boundAgent, env map[string]string) protocol.TaskAssignment {
	now := run.CreatedAt
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	model := bound.agent.Model
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
