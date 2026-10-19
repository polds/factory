package server

import (
	"context"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/handoff"
	"github.com/polds/factory/internal/inference"
	"github.com/polds/factory/internal/store"
)

func TestTaskEnvDefaultGitIdentity(t *testing.T) {
	s := &Server{
		cfg: Config{
			TaskServerURL: "http://172.17.0.1:8080",
			Secrets:       map[string]string{"GH_TOKEN": "gh-test"},
		},
		loaded: loaded{tree: &definition.Tree{
			Factory: definition.Factory{Secrets: []string{"GH_TOKEN"}},
		}},
	}
	env, err := s.taskEnv(context.Background(), store.Run{ID: "run_test", Prompt: "edit greet"}, boundAgent{
		agent: definition.Agent{MaxTurns: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GIT_AUTHOR_NAME":     "Agent",
		"GIT_AUTHOR_EMAIL":    "agent@example.com",
		"GIT_COMMITTER_NAME":  "Agent",
		"GIT_COMMITTER_EMAIL": "agent@example.com",
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("%s = %q, want %q", key, env[key], value)
		}
	}
	if strings.Contains(env["FACTORY_SECRET_VALUES"], "agent@example.com") || strings.Contains(env["FACTORY_SECRET_VALUES"], "Agent") {
		t.Fatalf("git identity must not be treated as a secret: %q", env["FACTORY_SECRET_VALUES"])
	}
}

func TestTaskEnvGitIdentityYieldsToAllowlistedSecret(t *testing.T) {
	s := &Server{
		cfg: Config{
			TaskServerURL: "http://172.17.0.1:8080",
			Secrets: map[string]string{
				"GIT_AUTHOR_NAME":  "Custom",
				"GIT_AUTHOR_EMAIL": "custom@example.com",
			},
		},
		loaded: loaded{tree: &definition.Tree{
			Factory: definition.Factory{Secrets: []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"}},
		}},
	}
	env, err := s.taskEnv(context.Background(), store.Run{ID: "run_test", Prompt: "edit greet"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if env["GIT_AUTHOR_NAME"] != "Custom" || env["GIT_AUTHOR_EMAIL"] != "custom@example.com" {
		t.Fatalf("allowlisted identity overwritten: author %q <%s>", env["GIT_AUTHOR_NAME"], env["GIT_AUTHOR_EMAIL"])
	}
	if env["GIT_COMMITTER_NAME"] != "Agent" || env["GIT_COMMITTER_EMAIL"] != "agent@example.com" {
		t.Fatalf("missing committer default: %q <%s>", env["GIT_COMMITTER_NAME"], env["GIT_COMMITTER_EMAIL"])
	}
}

func TestTaskEnvExtraHeaders(t *testing.T) {
	const planted = "sk-planted-extra-header-value"
	headers, err := inference.ParseExtraHeaders("x-litellm-api-key: Bearer " + planted)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: Config{
			TaskServerURL:         "http://172.17.0.1:8080",
			InferenceExtraHeaders: headers,
		},
		loaded: loaded{tree: &definition.Tree{}},
	}
	env, err := s.taskEnv(context.Background(), store.Run{ID: "run_test", Prompt: "edit greet"}, boundAgent{
		agent: definition.Agent{MaxTurns: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENAI_API_KEY"] != "" {
		t.Fatal("empty api key was copied into the task")
	}
	if env[inference.ExtraHeadersEnv] == "" {
		t.Fatal("extra headers were not copied into the task")
	}
	if !strings.Contains(env["FACTORY_SECRET_VALUES"], planted) {
		t.Fatal("header value missing from secret list")
	}
	again, err := inference.ParseExtraHeaders(env[inference.ExtraHeadersEnv])
	if err != nil || len(again) != 1 || again[0].Name != "x-litellm-api-key" {
		t.Fatal("task env headers did not parse")
	}
	red := s.redact("proxy said " + planted)
	if strings.Contains(red, planted) {
		t.Fatal("server redact left the header value")
	}
	prompt := handoff.BuildPrompt(definition.Agent{Name: "impl", Body: "token " + planted}, handoff.Context{Env: s.redactEnv()})
	if strings.Contains(prompt, planted) {
		t.Fatal("prompt contains header value")
	}
}
