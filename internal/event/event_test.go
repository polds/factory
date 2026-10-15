package event

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUnmarshalDocsExample(t *testing.T) {
	t.Parallel()
	const raw = `{
	  "provider": "github",
	  "event": "issue_labeled",
	  "delivery_id": "gh-1",
	  "occurred_at": "2026-01-02T03:04:05Z",
	  "actor": {"provider_id": "123", "handle": "octocat", "factory_user_id": null},
	  "subject": {
	    "kind": "issue",
	    "id": "I_1",
	    "number": 412,
	    "title": "Pay",
	    "url": "https://example.test/412",
	    "repo": {"code_forge": "GITHUB", "owner": "acme", "name": "payments-service"},
	    "labels": ["factory-ready"],
	    "branch": null,
	    "base_branch": null
	  },
	  "thread": {"kind": "github_issue", "external_ref": "acme/payments-service#412"},
	  "raw": {"action": "labeled", "label": {"name": "factory-ready"}}
	}`
	var got Event
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != ProviderGitHub || got.Event != "issue_labeled" || got.DeliveryID != "gh-1" {
		t.Fatalf("identity %+v", got)
	}
	if !got.OccurredAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("occurred_at %s", got.OccurredAt)
	}
	if got.Actor.ProviderID != "123" || got.Actor.Handle != "octocat" || got.Actor.FactoryUserID != nil {
		t.Fatalf("actor %+v", got.Actor)
	}
	if got.Subject.Kind != "issue" || got.Subject.Number != 412 || got.Subject.Repo.Owner != "acme" || got.Subject.Repo.Name != "payments-service" || got.Subject.Repo.CodeForge != "GITHUB" {
		t.Fatalf("subject %+v", got.Subject)
	}
	if len(got.Subject.Labels) != 1 || got.Subject.Labels[0] != "factory-ready" || got.Subject.Branch != nil || got.Subject.BaseBranch != nil {
		t.Fatalf("subject filters %+v", got.Subject)
	}
	if got.Thread.Kind != ThreadGitHubIssue || got.Thread.ExternalRef != "acme/payments-service#412" {
		t.Fatalf("thread %+v", got.Thread)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["action"] != "labeled" {
		t.Fatalf("raw %s", got.Raw)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()
	user := "fac_user"
	branch := "feature"
	base := "main"
	in := Event{
		Provider:   ProviderSlack,
		Event:      "message_posted",
		DeliveryID: "Ev1",
		OccurredAt: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC),
		Actor: Actor{
			ProviderID:    "U1",
			Handle:        "ada",
			FactoryUserID: &user,
		},
		Subject: Subject{
			Kind:       "message",
			ID:         "1.2",
			Number:     0,
			Title:      "hello",
			URL:        "https://slack.example/archives/C1/p1",
			Repo:       Repo{CodeForge: "GITHUB", Owner: "acme", Name: "payments-service"},
			Labels:     []string{"bug", "factory-ready"},
			Branch:     &branch,
			BaseBranch: &base,
			Paths:      []string{"services/payments/**"},
			Text:       "please look",
		},
		Thread: Thread{Kind: ThreadSlack, ExternalRef: "slack:T1/C1/1.2"},
		Raw:    json.RawMessage(`{"type":"message","text":"please look"}`),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !eventsEqual(in, got) {
		t.Fatalf("round trip\nin  %#v\nout %#v\njson %s", in, got, b)
	}
	if string(got.Raw) != `{"type":"message","text":"please look"}` {
		t.Fatalf("raw re-encoded %s", got.Raw)
	}
}

func TestThreadKindConstants(t *testing.T) {
	t.Parallel()
	kinds := []string{ThreadGitHubIssue, ThreadSlack, ThreadLinearIssue, ThreadWebhook, ThreadSchedule}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if kind == "" || seen[kind] {
			t.Fatalf("kind %q", kind)
		}
		seen[kind] = true
	}
}

func eventsEqual(a, b Event) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	var aj, bj any
	if json.Unmarshal(ab, &aj) != nil || json.Unmarshal(bb, &bj) != nil {
		return false
	}
	return jsonEqual(aj, bj)
}

func jsonEqual(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ab) == string(bb)
}
