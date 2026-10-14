package server

import (
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestResolveModelID(t *testing.T) {
	tree := &definition.Tree{Routers: map[string]definition.Router{
		"by-task": {
			Name:    "by-task",
			Type:    "prompt",
			Default: "prompt-default",
			Routes:  []definition.RouterRoute{{Description: "hard", Model: "prompt-route"}},
		},
		"by-size": {
			Name:       "by-size",
			Type:       "complexity",
			Default:    "complexity-default",
			Complexity: map[string]string{"easy": "easy-model", "medium": "medium-model", "hard": "hard-model"},
		},
		"bare": {
			Name:    "bare",
			Type:    "complexity",
			Default: "only-default",
		},
	}}
	cases := []struct {
		name  string
		agent definition.Agent
		want  string
	}{
		{name: "concrete", agent: definition.Agent{Model: "claude-x"}, want: "claude-x"},
		{name: "prompt router uses default", agent: definition.Agent{Model: "by-task"}, want: "prompt-default"},
		{name: "complexity uses medium", agent: definition.Agent{Model: "by-size"}, want: "medium-model"},
		{name: "complexity without medium uses default", agent: definition.Agent{Model: "bare"}, want: "only-default"},
		{name: "harness model", agent: definition.Agent{Harness: &definition.Harness{Type: "generic-openai", Model: "local-model"}}, want: "local-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveModelID(tree, tc.agent); got != tc.want {
				t.Fatalf("model %q, want %q", got, tc.want)
			}
		})
	}
	if got := resolveModelID(nil, definition.Agent{Model: "plain"}); got != "plain" {
		t.Fatalf("nil tree model %q", got)
	}
}
