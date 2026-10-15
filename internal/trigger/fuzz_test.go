package trigger

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func FuzzMatch(f *testing.F) {
	f.Add("github", "push", "acme/pay", "main", "alice", "factory-ready", "*.go", "src/a.go", `{"action":"opened","sender":{"type":"User"}}`, "opened")
	f.Add("slack", "message_posted", "T/C", "", "", "", "**", "", "", "")
	f.Fuzz(func(t *testing.T, provider, evName, repo, branch, author, label, glob, path, raw, action string) {
		if len(provider) > 40 || len(evName) > 80 || len(glob) > 128 || len(path) > 256 || len(raw) > 2048 || len(action) > 80 || len(label) > 80 {
			return
		}
		owner, name, _ := strings.Cut(repo, "/")
		m, _ := Compile(definition.Trigger{
			Provider: "github",
			Event:    "push",
			Filter: map[string]any{
				"repos":    []any{repo},
				"branches": []any{branch},
				"authors":  map[string]any{"not_in": []any{author}},
				"labels":   []any{label},
				"paths":    []any{glob},
				"keywords": []any{label},
			},
		})
		if m != nil {
			_ = m.Match(Event{
				Provider: "github",
				Event:    "push",
				Actor:    Actor{Handle: author},
				Subject: Subject{
					Repo:   Repo{Owner: owner, Name: name},
					Branch: branch,
					Labels: []string{label},
					Paths:  []string{path},
					Title:  label,
					Text:   action,
				},
				Raw: json.RawMessage(raw),
			})
		}
		_ = MatchGlob(glob, path)

		wh, _ := Compile(definition.Trigger{
			Provider: "webhook",
			Event:    "received",
			Filter: map[string]any{
				"webhook_ids": []any{"whk_fuzz"},
				"payload": map[string]any{
					"action": []any{action},
					"sender": map[string]any{"type": map[string]any{"not_in": []any{"Bot"}}},
				},
			},
		})
		if wh != nil {
			_ = wh.Match(Event{
				Provider: "webhook",
				Event:    "received",
				Thread:   Thread{Kind: "webhook", ExternalRef: "whk_fuzz"},
				Raw:      json.RawMessage(raw),
			})
		}

		junk, _ := Compile(definition.Trigger{
			Provider: provider,
			Event:    evName,
			Filter:   map[string]any{label: []any{author, glob}},
		})
		if junk != nil {
			_ = junk.Match(Event{Provider: provider, Event: evName, Raw: json.RawMessage(raw), Subject: Subject{Text: action, Title: label}})
		}
	})
}
