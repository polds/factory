package trigger

import (
	"encoding/json"
	"strings"
	"testing"

	syaml "sigs.k8s.io/yaml"

	"github.com/polds/factory/internal/definition"
)

func TestMatchRules(t *testing.T) {
	repo := Repo{Owner: "acme", Name: "payments-service"}
	base := Event{
		Provider: "github",
		Event:    "pull_request_opened",
		Actor:    Actor{Handle: "alice"},
		Subject: Subject{
			Repo:       repo,
			BaseBranch: "main",
			Labels:     []string{"needs-review", "bug"},
			Paths:      []string{"services/payments/api/pay.go"},
			Title:      "Fix checkout",
			Text:       "please look @bob",
			Number:     412,
		},
	}
	docs := filterYAML(t, `
repos: [acme/payments-service]
base_branches: [main]
paths: ["services/payments/**"]
authors: { not_in: [dependabot, renovate] }
labels: { in: [needs-review] }
`)
	cases := []struct {
		name    string
		filter  map[string]any
		mutate  func(Event) Event
		want    bool
		wantErr string
	}{
		{name: "docs example matches", filter: docs, want: true},
		{name: "and across keys fails one key", filter: docs, mutate: func(e Event) Event {
			e.Subject.Repo.Name = "other"
			return e
		}},
		{name: "or within key", filter: map[string]any{"labels": []any{"bug", "docs"}}, want: true},
		{name: "omitted key matches all", filter: map[string]any{"repos": []any{"acme/payments-service"}}, mutate: func(e Event) Event {
			e.Subject.Branch = "feature"
			return e
		}, want: true},
		{name: "bare list is in", filter: map[string]any{"labels": []any{"needs-review"}}, want: true},
		{name: "in object", filter: map[string]any{"labels": map[string]any{"in": []any{"needs-review"}}}, want: true},
		{name: "not_in misses", filter: map[string]any{"authors": map[string]any{"not_in": []any{"dependabot"}}}, want: true},
		{name: "not_in hits", filter: map[string]any{"authors": map[string]any{"not_in": []any{"alice"}}}},
		{name: "missing key fails in", filter: map[string]any{"authors": []any{"alice"}}, mutate: func(e Event) Event {
			e.Actor.Handle = ""
			return e
		}},
		{name: "missing key passes not_in", filter: map[string]any{"authors": map[string]any{"not_in": []any{"dependabot"}}}, mutate: func(e Event) Event {
			e.Actor.Handle = ""
			return e
		}, want: true},
		{name: "case sensitive", filter: map[string]any{"labels": []any{"Needs-Review"}}},
		{name: "keyword substring", filter: map[string]any{"keywords": []any{"checkout"}}, want: true},
		{name: "keyword case", filter: map[string]any{"keywords": []any{"Checkout"}}},
		{name: "keyword missing text fails in", filter: map[string]any{"keywords": []any{"checkout"}}, mutate: func(e Event) Event {
			e.Subject.Title = ""
			e.Subject.Text = ""
			return e
		}},
		{name: "paths glob", filter: map[string]any{"paths": []any{"services/payments/**"}}, want: true},
		{name: "paths miss", filter: map[string]any{"paths": []any{"docs/**"}}},
		{name: "paths missing fails in", filter: map[string]any{"paths": []any{"**/*.go"}}, mutate: func(e Event) Event {
			e.Subject.Paths = nil
			return e
		}},
		{name: "paths missing passes not_in", filter: map[string]any{"paths": map[string]any{"not_in": []any{"docs/**"}}}, mutate: func(e Event) Event {
			e.Subject.Paths = nil
			return e
		}, want: true},
		{name: "other event does not match", filter: docs, mutate: func(e Event) Event {
			e.Event = "push"
			return e
		}},
		{name: "pr number numeric", filter: map[string]any{"pr_numbers": []any{float64(412)}}, want: true},
		{name: "pr number string", filter: map[string]any{"pr_numbers": []any{"412"}}, want: true},
		{name: "mention", filter: map[string]any{"mentioned": []any{"bob"}}, want: true},
		{name: "mention miss", filter: map[string]any{"mentioned": []any{"carol"}}},
		{name: "unknown key", filter: map[string]any{"nope": []any{"x"}}, wantErr: `unknown filter key "nope"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := Compile(definition.Trigger{Provider: "github", Event: "pull_request_opened", Filter: tc.filter})
			if tc.wantErr != "" {
				if m != nil || !diagText(diags, tc.wantErr) {
					t.Fatalf("matcher=%v diags=%v", m, diags)
				}
				return
			}
			if errText := errorText(diags); errText != "" {
				t.Fatal(errText)
			}
			ev := base
			if tc.mutate != nil {
				ev = tc.mutate(base)
			}
			if got := m.Match(ev); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthorIsContentAuthor(t *testing.T) {
	m := mustCompile(t, "github", "issue_labeled", map[string]any{
		"authors": map[string]any{"not_in": []any{"dependabot", "renovate"}},
	})
	ev := Event{
		Provider: "github",
		Event:    "issue_labeled",
		Actor:    Actor{Handle: "alice"},
		Raw:      json.RawMessage(`{"label":{"name":"factory-ready"},"issue":{"user":{"login":"dependabot"}}}`),
	}
	if m.Match(ev) {
		t.Fatal("dependabot-authored issue matched authors not_in")
	}
	ev.Raw = json.RawMessage(`{"label":{"name":"factory-ready"},"issue":{"user":{"login":"alice"}}}`)
	if !m.Match(ev) {
		t.Fatal("human-authored issue was excluded")
	}
}

func TestMultiValueNotIn(t *testing.T) {
	m := mustCompile(t, "github", "pull_request_opened", map[string]any{
		"labels": map[string]any{"not_in": []any{"bug"}},
	})
	ev := Event{
		Provider: "github",
		Event:    "pull_request_opened",
		Subject:  Subject{Labels: []string{"bug", "docs"}},
	}
	if m.Match(ev) {
		t.Fatal("not_in matched an event that carries an excluded label")
	}
	ev.Subject.Labels = []string{"docs"}
	if !m.Match(ev) {
		t.Fatal("not_in rejected a label list that does not contain the excluded value")
	}
}

func TestLabeledUsesAddedLabel(t *testing.T) {
	filter := map[string]any{"labels": []any{"factory-ready"}}
	m := mustCompile(t, "github", "issue_labeled", filter)
	withAdded := Event{
		Provider: "github",
		Event:    "issue_labeled",
		Subject:  Subject{Labels: []string{"bug", "factory-ready"}},
		Raw:      json.RawMessage(`{"label":{"name":"docs"}}`),
	}
	if m.Match(withAdded) {
		t.Fatal("matched the full label list instead of the added label")
	}
	withAdded.Raw = json.RawMessage(`{"label":{"name":"factory-ready"}}`)
	if !m.Match(withAdded) {
		t.Fatal("added label did not match")
	}
	missing := withAdded
	missing.Raw = nil
	if m.Match(missing) {
		t.Fatal("missing added label matched in")
	}
	notIn := mustCompile(t, "github", "issue_labeled", map[string]any{
		"labels": map[string]any{"not_in": []any{"docs"}},
	})
	if !notIn.Match(missing) {
		t.Fatal("missing added label failed not_in")
	}
}

func TestPayloadRules(t *testing.T) {
	body := json.RawMessage(`{
		"action": "opened",
		"pull_request": {"draft": false, "labels": [{"name": "bug"}, {"name": "docs"}]},
		"sender": {"type": "User"},
		"deployment": {"url": "https://example.test"}
	}`)
	ev := Event{
		Provider: "webhook", Event: "received",
		Thread: Thread{Kind: "webhook", ExternalRef: "whk_01"},
		Raw:    body,
	}
	cases := []struct {
		name   string
		yaml   string
		raw    json.RawMessage
		ref    string
		want   bool
		errSub string
	}{
		{
			name: "docs payload",
			yaml: `
webhook_ids: [whk_01]
payload:
  action: [opened, reopened]
  pull_request:
    draft: [false]
  sender:
    type: { not_in: [Bot] }
  deployment:
    url: { exists: true }
`,
			want: true,
		},
		{
			name: "string false is not bool false",
			yaml: `
webhook_ids: [whk_01]
payload:
  pull_request:
    draft: ["false"]
`,
		},
		{
			name: "missing key fails in",
			yaml: `
webhook_ids: [whk_01]
payload:
  missing: [x]
`,
		},
		{
			name: "missing key passes not_in",
			yaml: `
webhook_ids: [whk_01]
payload:
  missing: { not_in: [x] }
`,
			want: true,
		},
		{
			name: "array of objects any element",
			yaml: `
webhook_ids: [whk_01]
payload:
  pull_request:
    labels:
      name: [bug]
`,
			want: true,
		},
		{
			name: "exists false on present url",
			yaml: `
webhook_ids: [whk_01]
payload:
  deployment:
    url: { exists: false }
`,
		},
		{
			name: "exists false on absent",
			yaml: `
webhook_ids: [whk_01]
payload:
  deployment:
    missing: { exists: false }
`,
			want: true,
		},
		{
			name: "case sensitive payload",
			yaml: `
webhook_ids: [whk_01]
payload:
  action: [Opened]
`,
		},
		{
			name: "wrong webhook id",
			yaml: `
webhook_ids: [whk_other]
payload:
  action: [opened]
`,
		},
		{
			name:   "two webhook ids",
			yaml:   `webhook_ids: [whk_01, whk_02]`,
			errSub: "exactly one",
		},
		{
			name:   "webhook not_in rejected",
			yaml:   `webhook_ids: { not_in: [whk_01] }`,
			errSub: "in only",
		},
		{
			name:   "exists outside payload",
			yaml:   `webhook_ids: { exists: true }`,
			errSub: "exists is only valid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, diags := Compile(definition.Trigger{
				Provider: "webhook", Event: "received", Filter: filterYAML(t, tc.yaml),
			})
			if tc.errSub != "" {
				if m != nil || !diagText(diags, tc.errSub) {
					t.Fatalf("matcher=%v diags=%v", m, diags)
				}
				return
			}
			if errText := errorText(diags); errText != "" {
				t.Fatal(errText)
			}
			gotEv := ev
			if tc.raw != nil {
				gotEv.Raw = tc.raw
			}
			if tc.ref != "" {
				gotEv.Thread.ExternalRef = tc.ref
			}
			if got := m.Match(gotEv); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProviderKeys(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		event    string
		filter   map[string]any
		ev       Event
		want     bool
	}{
		{
			name: "slack channel from ref", provider: "slack", event: "message_posted",
			filter: map[string]any{"channels": []any{"C123"}},
			ev:     Event{Provider: "slack", Event: "message_posted", Thread: Thread{ExternalRef: "slack:T1/C123/1.2"}},
			want:   true,
		},
		{
			name: "slack emoji", provider: "slack", event: "reaction_added",
			filter: map[string]any{"emojis": []any{"thumbsup"}},
			ev:     Event{Provider: "slack", Event: "reaction_added", Raw: json.RawMessage(`{"event":{"reaction":"thumbsup"}}`)},
			want:   true,
		},
		{
			name: "linear team", provider: "linear", event: "issue_created",
			filter: map[string]any{"teams": []any{"team_1"}},
			ev:     Event{Provider: "linear", Event: "issue_created", Raw: json.RawMessage(`{"data":{"teamId":"team_1"}}`)},
			want:   true,
		},
		{
			name: "jira project", provider: "jira", event: "issue_created",
			filter: map[string]any{"project_keys": []any{"PAY"}},
			ev:     Event{Provider: "jira", Event: "issue_created", Raw: json.RawMessage(`{"issue":{"fields":{"project":{"key":"PAY"}}}}`)},
			want:   true,
		},
		{
			name: "gitlab action", provider: "gitlab", event: "merge_request",
			filter: map[string]any{"actions": []any{"open"}},
			ev:     Event{Provider: "gitlab", Event: "merge_request", Raw: json.RawMessage(`{"object_attributes":{"action":"open"}}`)},
			want:   true,
		},
		{
			name: "azure source repo", provider: "azure_devops", event: "push",
			filter: map[string]any{"source_repos": []any{"payments"}},
			ev:     Event{Provider: "azure_devops", Event: "push", Raw: json.RawMessage(`{"resource":{"sourceRepository":{"name":"payments"}}}`)},
			want:   true,
		},
		{
			name: "factory stage", provider: "factory", event: "work_item_stage_changed",
			filter: map[string]any{"stages": []any{"building"}},
			ev:     Event{Provider: "factory", Event: "work_item_stage_changed", Subject: Subject{Kind: "building"}},
			want:   true,
		},
		{
			name: "schedule empty filter", provider: "schedule", event: "cron_fired",
			ev:   Event{Provider: "schedule", Event: "cron_fired"},
			want: true,
		},
		{
			name: "github reviewers", provider: "github", event: "pull_request_review_requested",
			filter: map[string]any{"reviewers": []any{"pat"}},
			ev:     Event{Provider: "github", Event: "pull_request_review_requested", Raw: json.RawMessage(`{"pull_request":{"requested_reviewers":[{"login":"pat"}]}}`)},
			want:   true,
		},
		{
			name: "review state", provider: "github", event: "pull_request_review_submitted",
			filter: map[string]any{"review_states": []any{"approved"}},
			ev:     Event{Provider: "github", Event: "pull_request_review_submitted", Raw: json.RawMessage(`{"review":{"state":"approved"}}`)},
			want:   true,
		},
		{
			name: "conclusion", provider: "github", event: "workflow_run_completed",
			filter: map[string]any{"conclusions": []any{"success"}, "workflows": []any{"ci"}},
			ev:     Event{Provider: "github", Event: "workflow_run_completed", Raw: json.RawMessage(`{"workflow_run":{"name":"ci","conclusion":"success"}}`)},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustCompile(t, tc.provider, tc.event, tc.filter)
			if got := m.Match(tc.ev); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAliasResolver(t *testing.T) {
	tr := definition.Trigger{
		Provider: "slack", Event: "message_posted",
		Filter: map[string]any{"channels": []any{"eng-payments"}},
	}
	r := mapResolver{"slack": {"channels": {"eng-payments": "C123"}}}
	m, diags := CompileWith(tr, r)
	if errorText(diags) != "" {
		t.Fatal(diags)
	}
	ev := Event{Provider: "slack", Event: "message_posted", Thread: Thread{ExternalRef: "slack:T/C123/1"}}
	if !m.Match(ev) {
		t.Fatal("resolved channel did not match")
	}
	if m.Match(Event{Provider: "slack", Event: "message_posted", Thread: Thread{ExternalRef: "slack:T/eng-payments/1"}}) {
		t.Fatal("alias literal matched after resolution")
	}

	missing := mapResolver{}
	m, diags = CompileWith(tr, missing)
	if !diagText(diags, `unresolved slack channels alias "eng-payments"`) {
		t.Fatalf("diags %v", diags)
	}
	if m == nil || !m.Match(Event{Provider: "slack", Event: "message_posted", Thread: Thread{ExternalRef: "slack:T/eng-payments/1"}}) {
		t.Fatal("unresolved alias should keep the literal")
	}

	both := definition.Trigger{
		Provider: "slack", Event: "message_posted",
		Filter: map[string]any{"channels": []any{"eng"}, "channel_ids": []any{"C1"}},
	}
	if _, diags := Compile(both); !diagText(diags, "mutually exclusive") {
		t.Fatalf("diags %v", diags)
	}
}

type mapResolver map[string]map[string]map[string]string

func (r mapResolver) Resolve(provider, aliasKey, name string) (string, bool) {
	id, ok := r[provider][aliasKey][name]
	return id, ok
}

func TestFilterBounds(t *testing.T) {
	vals := make([]any, 33)
	for i := range vals {
		vals[i] = "v"
	}
	if _, diags := Compile(definition.Trigger{Provider: "github", Event: "push", Filter: map[string]any{"repos": vals}}); !diagText(diags, "limit is 32") {
		t.Fatalf("diags %v", diags)
	}
	okVals := vals[:32]
	if _, diags := Compile(definition.Trigger{Provider: "github", Event: "push", Filter: map[string]any{"repos": okVals}}); errorText(diags) != "" {
		t.Fatal(diags)
	}

	deep := nestPayload(11)
	if _, diags := Compile(definition.Trigger{Provider: "webhook", Event: "received", Filter: map[string]any{
		"webhook_ids": []any{"whk"},
		"payload":     deep,
	}}); !diagText(diags, "exceeds 10") {
		t.Fatalf("diags %v", diags)
	}
	if _, diags := Compile(definition.Trigger{Provider: "webhook", Event: "received", Filter: map[string]any{
		"webhook_ids": []any{"whk"},
		"payload":     nestPayload(10),
	}}); errorText(diags) != "" {
		t.Fatal(diags)
	}

	leaves := map[string]any{}
	for i := 0; i < 65; i++ {
		leaves[string(rune('a'+i%26))+string(rune('a'+i/26))] = []any{"x"}
	}
	if _, diags := Compile(definition.Trigger{Provider: "webhook", Event: "received", Filter: map[string]any{
		"webhook_ids": []any{"whk"},
		"payload":     leaves,
	}}); !diagText(diags, "limit is 64") {
		t.Fatalf("diags %v", diags)
	}
}

func nestPayload(depth int) map[string]any {
	var cur any = []any{"x"}
	for i := 0; i < depth; i++ {
		cur = map[string]any{"k": cur}
	}
	m, _ := cur.(map[string]any)
	return m
}

func mustCompile(t *testing.T, provider, event string, filter map[string]any) Matcher {
	t.Helper()
	m, diags := Compile(definition.Trigger{Provider: provider, Event: event, Filter: filter})
	if m == nil || errorText(diags) != "" {
		t.Fatalf("compile: %v", diags)
	}
	return m
}

func filterYAML(t *testing.T, src string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := syaml.Unmarshal([]byte(src), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func diagText(ds []definition.Diagnostic, sub string) bool {
	for _, d := range ds {
		if strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}

func errorText(ds []definition.Diagnostic) string {
	for _, d := range ds {
		if d.Severity == definition.SeverityError {
			return d.Message
		}
	}
	return ""
}
