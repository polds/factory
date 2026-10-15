package github

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIssueLabeledListsEveryLabelAndKeepsAddedName(t *testing.T) {
	t.Parallel()
	body := `{
		"action": "labeled",
		"label": {"name": "factory-ready"},
		"issue": {
			"id": 4120,
			"number": 412,
			"title": "Add refunds",
			"body": "Refund link is broken.",
			"html_url": "https://github.com/acme/payments-service/issues/412",
			"created_at": "2026-01-02T03:04:05Z",
			"updated_at": "2026-01-02T03:05:06Z",
			"labels": [{"name": "bug"}, {"name": "factory-ready"}]
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
	events, err := Normalize(delivery("issues", "dlv_label", body))
	got := mustOne(t, events, err)
	if got.Event != "issue_labeled" || got.Provider != "github" || got.DeliveryID != "dlv_label" {
		t.Fatalf("identity = %+v", got)
	}
	if got.Thread.Kind != "github_issue" || got.Thread.ExternalRef != "acme/payments-service#412" {
		t.Fatalf("thread = %+v", got.Thread)
	}
	if strings.Join(got.Subject.Labels, ",") != "bug,factory-ready" {
		t.Fatalf("subject.labels = %v, want every current label", got.Subject.Labels)
	}
	added := rawLabelName(t, got.Raw)
	if added != "factory-ready" {
		t.Fatalf("raw.label.name = %q, want the added label", added)
	}
	if got.Subject.Kind != "issue" || got.Subject.ID != "4120" || got.Subject.Number != 412 {
		t.Fatalf("subject identity = %+v", got.Subject)
	}
	if got.Subject.Repo == nil || got.Subject.Repo.CodeForge != "GITHUB" ||
		got.Subject.Repo.Owner != "acme" || got.Subject.Repo.Name != "payments-service" {
		t.Fatalf("repo = %+v", got.Subject.Repo)
	}
	if got.Subject.Branch != nil || got.Subject.BaseBranch != nil {
		t.Fatalf("issue branch = %v / %v, want null", got.Subject.Branch, got.Subject.BaseBranch)
	}
	if got.Actor.Handle != "octocat" || got.Actor.ProviderID != "1" || got.Actor.FactoryUserID != nil {
		t.Fatalf("actor = %+v", got.Actor)
	}
	wantAt := time.Date(2026, 1, 2, 3, 5, 6, 0, time.UTC)
	if !got.OccurredAt.Equal(wantAt) {
		t.Fatalf("occurred_at = %s", got.OccurredAt)
	}
	if got.Subject.Text != "Refund link is broken." {
		t.Fatalf("text = %q", got.Subject.Text)
	}
}

func TestEventMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ghEvent string
		body    string
		want    []string
	}{
		{"issues opened", "issues", issueAction("opened"), []string{"issue_created"}},
		{"issues assigned", "issues", issueAction("assigned"), []string{"issue_assigned"}},
		{"issues labeled", "issues", issueAction("labeled"), []string{"issue_labeled"}},
		{"issues edited ignored", "issues", issueAction("edited"), nil},
		{"comment on issue", "issue_comment", commentBody(false, "Looks good."), []string{"issue_commented"}},
		{"comment on pull", "issue_comment", commentBody(true, "Looks good."), []string{"pull_request_commented"}},
		{"comment edited ignored", "issue_comment", commentAction("edited", false, "x"), nil},
		{"pr opened", "pull_request", prAction("opened", false), []string{"pull_request_opened"}},
		{"pr closed", "pull_request", prAction("closed", false), []string{"pull_request_closed"}},
		{"pr merged", "pull_request", prAction("closed", true), []string{"pull_request_merged"}},
		{"pr reopened", "pull_request", prAction("reopened", false), []string{"pull_request_reopened"}},
		{"pr ready", "pull_request", prAction("ready_for_review", false), []string{"pull_request_ready"}},
		{"pr labeled", "pull_request", prAction("labeled", false), []string{"pull_request_labeled"}},
		{"pr assigned", "pull_request", prAction("assigned", false), []string{"pull_request_assigned"}},
		{"pr synchronize", "pull_request", prAction("synchronize", false), []string{"pull_request_synchronized"}},
		{"pr review requested", "pull_request", prAction("review_requested", false), []string{"pull_request_review_requested"}},
		{"pr enqueued", "pull_request", prAction("enqueued", false), []string{"pull_request_enqueued"}},
		{"pr dequeued", "pull_request", prAction("dequeued", false), []string{"pull_request_dequeued"}},
		{"pr edited ignored", "pull_request", prAction("edited", false), nil},
		{"review submitted", "pull_request_review", reviewBody("submitted", "Ship it."), []string{"pull_request_review_submitted"}},
		{"review dismissed ignored", "pull_request_review", reviewBody("dismissed", "nope"), nil},
		{"push", "push", pushBody(), []string{"push"}},
		{"check run rerequested", "check_run", checkRunBody("rerequested"), []string{"check_run_rerequested"}},
		{"check run completed ignored", "check_run", checkRunBody("completed"), nil},
		{"check suite completed", "check_suite", checkSuiteBody("completed"), []string{"check_suite_completed"}},
		{"check suite rerequested", "check_suite", checkSuiteBody("rerequested"), []string{"check_suite_rerequested"}},
		{"check suite requested ignored", "check_suite", checkSuiteBody("requested"), nil},
		{"workflow completed", "workflow_run", workflowBody("completed"), []string{"workflow_run_completed"}},
		{"workflow requested ignored", "workflow_run", workflowBody("requested"), nil},
		{"installation created", "installation", installationBody("created"), []string{"installation_changed"}},
		{"installation deleted", "installation", installationBody("deleted"), []string{"installation_changed"}},
		{"installation repos", "installation_repositories", installationReposBody(), []string{"installation_changed"}},
		{"ping ignored", "ping", `{"zen":"keep it logically awesome"}`, nil},
		{"unknown ignored", "star", `{"action":"created"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(delivery(tc.ghEvent, "dlv_"+tc.name, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(eventNames(got), ",") != strings.Join(tc.want, ",") {
				t.Fatalf("events = %v, want %v", eventNames(got), tc.want)
			}
		})
	}
}

func TestMentionDerivedFromBody(t *testing.T) {
	t.Parallel()
	n := Normalizer{AppSlug: "factory-dev"}
	got, err := n.Normalize(delivery("issue_comment", "dlv_mention", commentBody(false, "Hey @factory-dev look at this.")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(got), ",") != "issue_commented,issue_mentioned" {
		t.Fatalf("events = %v", eventNames(got))
	}
	if got[1].Thread.ExternalRef != got[0].Thread.ExternalRef {
		t.Fatalf("mentioned thread = %s, primary = %s", got[1].Thread.ExternalRef, got[0].Thread.ExternalRef)
	}

	pr, err := n.Normalize(delivery("pull_request", "dlv_pr_mention", prBodyWithText("opened", false, "cc @Factory-Dev[bot]")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(pr), ",") != "pull_request_opened,pull_request_mentioned" {
		t.Fatalf("pr events = %v", eventNames(pr))
	}

	review, err := n.Normalize(delivery("pull_request_review", "dlv_review_mention", reviewBody("submitted", "@factory-dev ship it")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(review), ",") != "pull_request_review_submitted,pull_request_mentioned" {
		t.Fatalf("review events = %v", eventNames(review))
	}

	// A label action does not re-derive a mention from the existing issue body.
	labeled, err := n.Normalize(delivery("issues", "dlv_label_mention", issueWithBody("labeled", "please @factory-dev")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(labeled), ",") != "issue_labeled" {
		t.Fatalf("labeled events = %v", eventNames(labeled))
	}

	// Slug boundaries: a longer login or an email is not a mention.
	boundary, err := n.Normalize(delivery("issues", "dlv_boundary", issueWithBody("opened", "see @factory-dev-bot and dev@factory-dev.com")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(boundary), ",") != "issue_created" {
		t.Fatalf("boundary events = %v", eventNames(boundary))
	}

	// No slug configured means no derived mention.
	plain, err := Normalize(delivery("issues", "dlv_noslug", issueWithBody("opened", "hey @factory-dev")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eventNames(plain), ",") != "issue_created" {
		t.Fatalf("no-slug events = %v", eventNames(plain))
	}
}

func TestBotActorsCarryBotHandle(t *testing.T) {
	t.Parallel()
	bare, err := Normalize(delivery("issues", "dlv_bot", issueFrom("opened", "renovate", "Bot")))
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) != 1 || bare[0].Actor.Handle != "renovate[bot]" {
		t.Fatalf("bare bot handle = %+v", actors(bare))
	}
	suffixed, err := Normalize(delivery("issues", "dlv_bot2", issueFrom("opened", "dependabot[bot]", "Bot")))
	if err != nil {
		t.Fatal(err)
	}
	if len(suffixed) != 1 || suffixed[0].Actor.Handle != "dependabot[bot]" {
		t.Fatalf("suffixed bot handle = %+v", actors(suffixed))
	}
	human, err := Normalize(delivery("issues", "dlv_human", issueFrom("opened", "octocat", "User")))
	if err != nil {
		t.Fatal(err)
	}
	if len(human) != 1 || human[0].Actor.Handle != "octocat" {
		t.Fatalf("human handle = %+v", actors(human))
	}
}

func TestPullRequestSubject(t *testing.T) {
	t.Parallel()
	events, err := Normalize(delivery("pull_request", "dlv_pr", prAction("opened", false)))
	got := mustOne(t, events, err)
	if got.Subject.Kind != "pull_request" || got.Subject.Number != 77 {
		t.Fatalf("subject = %+v", got.Subject)
	}
	if got.Thread.ExternalRef != "acme/payments-service#77" || got.Thread.Kind != "github_issue" {
		t.Fatalf("thread = %+v", got.Thread)
	}
	if got.Subject.Branch == nil || *got.Subject.Branch != "refunds" {
		t.Fatalf("branch = %v", got.Subject.Branch)
	}
	if got.Subject.BaseBranch == nil || *got.Subject.BaseBranch != "main" {
		t.Fatalf("base = %v", got.Subject.BaseBranch)
	}
	if strings.Join(got.Subject.Labels, ",") != "needs-review" {
		t.Fatalf("labels = %v", got.Subject.Labels)
	}
}

func TestPushPathsAndRef(t *testing.T) {
	t.Parallel()
	events, err := Normalize(delivery("push", "dlv_push", pushBody()))
	got := mustOne(t, events, err)
	if got.Subject.Kind != "push" || got.Subject.ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("subject = %+v", got.Subject)
	}
	if got.Thread.Kind != "github_push" || got.Thread.ExternalRef != "acme/payments-service@refs/heads/main" {
		t.Fatalf("thread = %+v", got.Thread)
	}
	if got.Subject.Branch == nil || *got.Subject.Branch != "main" {
		t.Fatalf("branch = %v", got.Subject.Branch)
	}
	if strings.Join(got.Subject.Paths, ",") != "a.go,b.go,c.go" {
		t.Fatalf("paths = %v", got.Subject.Paths)
	}
	if got.Subject.Text != "fix refunds" {
		t.Fatalf("text = %q", got.Subject.Text)
	}
}

func TestCheckRunThreadUsesSinglePullRequest(t *testing.T) {
	t.Parallel()
	events, err := Normalize(delivery("check_run", "dlv_cr", checkRunBody("rerequested")))
	one := mustOne(t, events, err)
	if one.Thread.ExternalRef != "acme/payments-service#77" || one.Thread.Kind != "github_issue" {
		t.Fatalf("single pr thread = %+v", one.Thread)
	}
	if one.Subject.Kind != "check_run" || one.Subject.Title != "ci" {
		t.Fatalf("subject = %+v", one.Subject)
	}

	manyEvents, err := Normalize(delivery("check_run", "dlv_cr2", checkRunManyPRs()))
	many := mustOne(t, manyEvents, err)
	if many.Thread.Kind != "github_check" || many.Thread.ExternalRef != "acme/payments-service@abc123" {
		t.Fatalf("ambiguous pr thread = %+v", many.Thread)
	}
}

func TestRepoUsesFullNameWhenOwnerLoginMissing(t *testing.T) {
	t.Parallel()
	body := `{
		"action": "opened",
		"issue": {
			"id": 1, "number": 9, "title": "t", "body": "",
			"html_url": "https://github.com/acme/payments-service/issues/9",
			"updated_at": "2026-01-02T03:05:06Z"
		},
		"repository": {"name": "payments-service", "full_name": "acme/payments-service", "owner": {"login": ""}},
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
	events, err := Normalize(delivery("issues", "dlv_fullname", body))
	got := mustOne(t, events, err)
	if got.Thread.ExternalRef != "acme/payments-service#9" {
		t.Fatalf("thread = %+v", got.Thread)
	}
}

func TestInstallationWithSeveralReposOmitsSubjectRepo(t *testing.T) {
	t.Parallel()
	body := `{
		"action": "created",
		"installation": {
			"id": 42,
			"account": {"login": "acme", "id": 10, "type": "Organization"},
			"updated_at": "2026-01-02T11:00:00Z"
		},
		"repositories": [
			{"full_name": "acme/payments-service"},
			{"full_name": "acme/other"}
		],
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
	events, err := Normalize(delivery("installation", "dlv_inst", body))
	got := mustOne(t, events, err)
	if got.Event != "installation_changed" || got.Subject.Repo != nil {
		t.Fatalf("subject repo = %+v event %s", got.Subject.Repo, got.Event)
	}
	if got.Thread.ExternalRef != "installation:42" {
		t.Fatalf("thread = %+v", got.Thread)
	}
}

func TestHeaderLookupIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	d := delivery("issues", "dlv_case", issueAction("opened"))
	d.Headers = map[string]string{"x-github-event": "issues"}
	events, err := Normalize(d)
	got := mustOne(t, events, err)
	if got.Event != "issue_created" {
		t.Fatalf("event = %s", got.Event)
	}
}

func TestRejectsBadDeliveries(t *testing.T) {
	t.Parallel()
	if _, err := Normalize(delivery("", "dlv", `{}`)); err == nil {
		t.Fatal("missing X-GitHub-Event returned nil error")
	}
	if _, err := Normalize(delivery("issues", "dlv", `{`)); err == nil {
		t.Fatal("invalid JSON returned nil error")
	}
	if _, err := Normalize(Delivery{Headers: map[string]string{"X-GitHub-Event": "issues"}}); err == nil {
		t.Fatal("empty body returned nil error")
	}
}

const repoJSON = `{
	"id": 100,
	"name": "payments-service",
	"full_name": "acme/payments-service",
	"owner": {"login": "acme", "id": 10, "type": "Organization"}
}`

func delivery(event, id, body string) Delivery {
	return Delivery{
		DeliveryID: id,
		Headers:    map[string]string{"X-GitHub-Event": event},
		Body:       []byte(body),
	}
}

func issueAction(action string) string {
	return issueWithBody(action, "Please refund.")
}

func issueWithBody(action, text string) string {
	return `{
		"action": "` + action + `",
		"label": {"name": "factory-ready"},
		"issue": {
			"id": 4120, "number": 412, "title": "Add refunds",
			"body": ` + jsonString(text) + `,
			"html_url": "https://github.com/acme/payments-service/issues/412",
			"updated_at": "2026-01-02T03:05:06Z",
			"labels": [{"name": "bug"}, {"name": "factory-ready"}]
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func issueFrom(action, login, typ string) string {
	return `{
		"action": "` + action + `",
		"issue": {
			"id": 1, "number": 9, "title": "t", "body": "",
			"html_url": "https://github.com/acme/payments-service/issues/9",
			"updated_at": "2026-01-02T03:05:06Z"
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": ` + jsonString(login) + `, "id": 50, "type": "` + typ + `"}
	}`
}

func commentBody(onPR bool, text string) string {
	return commentAction("created", onPR, text)
}

func commentAction(action string, onPR bool, text string) string {
	pr := ""
	if onPR {
		pr = `,"pull_request": {"url": "https://api.github.com/repos/acme/payments-service/pulls/412"}`
	}
	return `{
		"action": "` + action + `",
		"issue": {
			"id": 4120, "number": 412, "title": "Add refunds",
			"html_url": "https://github.com/acme/payments-service/issues/412"
			` + pr + `
		},
		"comment": {
			"id": 900, "body": ` + jsonString(text) + `,
			"html_url": "https://github.com/acme/payments-service/issues/412#issuecomment-900",
			"created_at": "2026-01-02T04:00:00Z",
			"user": {"login": "octocat", "id": 1, "type": "User"}
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func prAction(action string, merged bool) string {
	return prBodyWithText(action, merged, "Open a refund path.")
}

func prBodyWithText(action string, merged bool, text string) string {
	mergedJSON := "false"
	if merged {
		mergedJSON = "true"
	}
	return `{
		"action": "` + action + `",
		"label": {"name": "needs-review"},
		"number": 77,
		"pull_request": {
			"id": 7700, "number": 77, "title": "Refunds",
			"body": ` + jsonString(text) + `,
			"html_url": "https://github.com/acme/payments-service/pull/77",
			"merged": ` + mergedJSON + `,
			"updated_at": "2026-01-02T05:00:00Z",
			"labels": [{"name": "needs-review"}],
			"head": {"ref": "refunds", "sha": "abc123"},
			"base": {"ref": "main", "sha": "def456"}
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func reviewBody(action, text string) string {
	return `{
		"action": "` + action + `",
		"review": {
			"id": 55, "body": ` + jsonString(text) + `,
			"state": "approved",
			"html_url": "https://github.com/acme/payments-service/pull/77#pullrequestreview-55",
			"submitted_at": "2026-01-02T06:00:00Z"
		},
		"pull_request": {
			"id": 7700, "number": 77, "title": "Refunds",
			"html_url": "https://github.com/acme/payments-service/pull/77",
			"head": {"ref": "refunds"}, "base": {"ref": "main"}
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func pushBody() string {
	return `{
		"ref": "refs/heads/main",
		"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"before": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"},
		"head_commit": {
			"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"message": "fix refunds",
			"timestamp": "2026-01-02T07:00:00Z",
			"url": "https://github.com/acme/payments-service/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"commits": [{
			"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"added": ["a.go"],
			"removed": ["b.go"],
			"modified": ["c.go", "a.go"]
		}]
	}`
}

func checkRunBody(action string) string {
	return `{
		"action": "` + action + `",
		"check_run": {
			"id": 321, "name": "ci", "head_sha": "abc123",
			"html_url": "https://github.com/acme/payments-service/runs/321",
			"status": "queued",
			"started_at": "2026-01-02T08:00:00Z",
			"pull_requests": [{
				"number": 77,
				"head": {"ref": "refunds", "sha": "abc123"},
				"base": {"ref": "main"}
			}],
			"check_suite": {"head_branch": "refunds", "head_sha": "abc123"}
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "github-actions[bot]", "id": 41898282, "type": "Bot"}
	}`
}

func checkRunManyPRs() string {
	return `{
		"action": "rerequested",
		"check_run": {
			"id": 322, "name": "ci", "head_sha": "abc123",
			"html_url": "https://github.com/acme/payments-service/runs/322",
			"started_at": "2026-01-02T08:00:00Z",
			"pull_requests": [
				{"number": 77, "head": {"ref": "refunds"}, "base": {"ref": "main"}},
				{"number": 78, "head": {"ref": "other"}, "base": {"ref": "main"}}
			]
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func checkSuiteBody(action string) string {
	return `{
		"action": "` + action + `",
		"check_suite": {
			"id": 654, "head_branch": "main", "head_sha": "abc123",
			"conclusion": "success", "updated_at": "2026-01-02T09:00:00Z",
			"pull_requests": []
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func workflowBody(action string) string {
	return `{
		"action": "` + action + `",
		"workflow": {"name": "CI", "path": ".github/workflows/ci.yml"},
		"workflow_run": {
			"id": 888, "name": "CI", "head_branch": "main",
			"head_sha": "abc123", "conclusion": "success",
			"html_url": "https://github.com/acme/payments-service/actions/runs/888",
			"updated_at": "2026-01-02T10:00:00Z",
			"pull_requests": []
		},
		"repository": ` + repoJSON + `,
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func installationBody(action string) string {
	return `{
		"action": "` + action + `",
		"installation": {
			"id": 42,
			"account": {"login": "acme", "id": 10, "type": "Organization"},
			"updated_at": "2026-01-02T11:00:00Z"
		},
		"repositories": [{"id": 100, "name": "payments-service", "full_name": "acme/payments-service"}],
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func installationReposBody() string {
	return `{
		"action": "added",
		"installation": {"id": 42, "account": {"login": "acme", "id": 10, "type": "Organization"}},
		"repositories_added": [{"id": 100, "name": "payments-service", "full_name": "acme/payments-service"}],
		"sender": {"login": "octocat", "id": 1, "type": "User"}
	}`
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func mustOne(t *testing.T, ev []Event, err error) Event {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 1 {
		t.Fatalf("len(events) = %d, want 1 (%v)", len(ev), eventNames(ev))
	}
	return ev[0]
}

func eventNames(ev []Event) []string {
	out := make([]string, len(ev))
	for i := range ev {
		out[i] = ev[i].Event
	}
	return out
}

func actors(ev []Event) []string {
	out := make([]string, len(ev))
	for i := range ev {
		out[i] = ev[i].Actor.Handle
	}
	return out
}

func rawLabelName(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var body struct {
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Label.Name
}
