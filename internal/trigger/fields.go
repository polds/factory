package trigger

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// valuesFor returns the event values for a canonical filter key.
// ok is false when the field is absent. An empty slice with ok true is a
// present field with no values.
//
// GitHub issue_labeled and pull_request_labeled compare labels to the added
// label at raw.label.name (T19). The subject's full label list is not the
// filter input for those events. Other events use subject.labels.
func valuesFor(e Event, key string) ([]string, bool) {
	fn := extractors[e.Provider][key]
	if fn == nil {
		return nil, false
	}
	return fn(e)
}

type extract func(Event) ([]string, bool)

var extractors = map[string]map[string]extract{
	"github": {
		"repos":          repoSlug,
		"branches":       branchName,
		"base_branches":  baseBranch,
		"paths":          pathList,
		"labels":         labels,
		"authors":        author,
		"assignees":      assignees,
		"mentioned":      mentioned,
		"reviewers":      reviewers,
		"reviewer_teams": reviewerTeams,
		"review_states":  reviewState,
		"workflows":      workflowName,
		"conclusions":    conclusion,
		"pr_numbers":     numberString,
	},
	"gitlab": {
		"repos":         repoSlug,
		"actions":       gitlabAction,
		"branches":      branchName,
		"base_branches": baseBranch,
		"mentioned":     mentioned,
	},
	"azure_devops": {
		"repos":           repoSlug,
		"source_repos":    sourceRepo,
		"branches":        branchName,
		"base_branches":   baseBranch,
		"work_item_types": workItemType,
		"labels":          labels,
		"authors":         author,
		"assignees":       assignees,
		"mentioned":       mentioned,
	},
	"slack": {
		"channel_ids":   slackChannel,
		"user_ids":      slackUser,
		"emojis":        slackEmoji,
		"item_user_ids": slackItemUser,
	},
	"linear": {
		"team_ids":           linearTeam,
		"project_ids":        linearProject,
		"state_ids":          linearState,
		"issue_ids":          linearIssue,
		"labels":             labels,
		"assignee_ids":       linearAssignee,
		"creator_ids":        linearCreator,
		"mentioned_user_ids": linearMentioned,
	},
	"jira": {
		"project_keys": jiraProject,
		"labels":       labels,
		"status_ids":   jiraStatus,
	},
	"webhook": {
		"webhook_ids": webhookID,
	},
	"factory": {
		"stages": factoryStage,
	},
}

func repoSlug(e Event) ([]string, bool) {
	if e.Subject.Repo.Owner != "" || e.Subject.Repo.Name != "" {
		return []string{e.Subject.Repo.Owner + "/" + e.Subject.Repo.Name}, true
	}
	if s, ok := rawString(e.Raw, "repository", "full_name"); ok {
		return []string{s}, true
	}
	return nil, false
}

func branchName(e Event) ([]string, bool) {
	if e.Subject.Branch != "" {
		return []string{e.Subject.Branch}, true
	}
	return nil, false
}

func baseBranch(e Event) ([]string, bool) {
	if e.Subject.BaseBranch != "" {
		return []string{e.Subject.BaseBranch}, true
	}
	return nil, false
}

func pathList(e Event) ([]string, bool) {
	if e.Subject.Paths == nil {
		return nil, false
	}
	return e.Subject.Paths, true
}

func labels(e Event) ([]string, bool) {
	if e.Provider == "github" && strings.HasSuffix(e.Event, "_labeled") {
		if s, ok := rawString(e.Raw, "label", "name"); ok {
			return []string{s}, true
		}
		return nil, false
	}
	if e.Subject.Labels != nil {
		return e.Subject.Labels, true
	}
	names := rawStrings(e.Raw, "data", "labels", "*", "name")
	if len(names) == 0 {
		names = rawStrings(e.Raw, "issue", "labels", "*", "name")
	}
	if len(names) == 0 {
		names = rawStrings(e.Raw, "issue", "fields", "labels", "*")
	}
	if len(names) == 0 {
		return nil, false
	}
	return names, true
}

func author(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "issue", "user", "login"); ok {
		return []string{s}, true
	}
	if s, ok := rawString(e.Raw, "pull_request", "user", "login"); ok {
		return []string{s}, true
	}
	if e.Actor.Handle != "" {
		return []string{e.Actor.Handle}, true
	}
	return nil, false
}

func assignees(e Event) ([]string, bool) {
	vals := rawStrings(e.Raw, "issue", "assignees", "*", "login")
	vals = append(vals, rawStrings(e.Raw, "pull_request", "assignees", "*", "login")...)
	if s, ok := rawString(e.Raw, "assignee", "login"); ok {
		vals = append(vals, s)
	}
	if len(vals) == 0 {
		return nil, false
	}
	return vals, true
}

var mentionRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9])@([A-Za-z0-9][A-Za-z0-9-]*)`)

func mentioned(e Event) ([]string, bool) {
	hay, ok := keywordHay(e)
	if !ok {
		return nil, false
	}
	found := mentionRE.FindAllStringSubmatch(hay, -1)
	if len(found) == 0 {
		return nil, false
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range found {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out, true
}

func keywordHay(e Event) (string, bool) {
	if e.Subject.Title == "" && e.Subject.Text == "" {
		return "", false
	}
	return e.Subject.Title + "\n" + e.Subject.Text, true
}

func reviewers(e Event) ([]string, bool) {
	vals := rawStrings(e.Raw, "pull_request", "requested_reviewers", "*", "login")
	if s, ok := rawString(e.Raw, "requested_reviewer", "login"); ok {
		vals = append(vals, s)
	}
	if len(vals) == 0 {
		return nil, false
	}
	return vals, true
}

func reviewerTeams(e Event) ([]string, bool) {
	vals := rawStrings(e.Raw, "pull_request", "requested_teams", "*", "slug")
	if len(vals) == 0 {
		return nil, false
	}
	return vals, true
}

func reviewState(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "review", "state"); ok {
		return []string{s}, true
	}
	return nil, false
}

func workflowName(e Event) ([]string, bool) {
	for _, path := range [][]string{
		{"workflow_run", "name"},
		{"workflow", "name"},
		{"check_run", "name"},
	} {
		if s, ok := rawString(e.Raw, path...); ok {
			return []string{s}, true
		}
	}
	return nil, false
}

func conclusion(e Event) ([]string, bool) {
	for _, path := range [][]string{
		{"workflow_run", "conclusion"},
		{"check_run", "conclusion"},
		{"check_suite", "conclusion"},
	} {
		if s, ok := rawString(e.Raw, path...); ok {
			return []string{s}, true
		}
	}
	return nil, false
}

func numberString(e Event) ([]string, bool) {
	if e.Subject.Number == 0 {
		return nil, false
	}
	return []string{strconv.Itoa(e.Subject.Number)}, true
}

func gitlabAction(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "object_attributes", "action"); ok {
		return []string{s}, true
	}
	return nil, false
}

func sourceRepo(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "resource", "sourceRepository", "name"); ok {
		return []string{s}, true
	}
	return nil, false
}

func workItemType(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "resource", "workItemType"); ok {
		return []string{s}, true
	}
	if e.Subject.Kind != "" {
		return []string{e.Subject.Kind}, true
	}
	return nil, false
}

func slackChannel(e Event) ([]string, bool) {
	if c, ok := slackRefChannel(e.Thread.ExternalRef); ok {
		return []string{c}, true
	}
	if s, ok := rawString(e.Raw, "event", "channel"); ok {
		return []string{s}, true
	}
	return nil, false
}

func slackRefChannel(ref string) (string, bool) {
	const prefix = "slack:"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(parts) < 2 || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func slackUser(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "event", "user"); ok {
		return []string{s}, true
	}
	if e.Actor.ProviderID != "" {
		return []string{e.Actor.ProviderID}, true
	}
	return nil, false
}

func slackEmoji(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "event", "reaction"); ok {
		return []string{s}, true
	}
	return nil, false
}

func slackItemUser(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "event", "item_user"); ok {
		return []string{s}, true
	}
	return nil, false
}

func firstRaw(e Event, paths [][]string) ([]string, bool) {
	for _, p := range paths {
		if s, ok := rawString(e.Raw, p...); ok {
			return []string{s}, true
		}
	}
	return nil, false
}

func linearTeam(e Event) ([]string, bool) {
	return firstRaw(e, [][]string{{"data", "teamId"}, {"data", "team", "id"}})
}

func linearProject(e Event) ([]string, bool) {
	return firstRaw(e, [][]string{{"data", "projectId"}, {"data", "project", "id"}})
}

func linearState(e Event) ([]string, bool) {
	return firstRaw(e, [][]string{{"data", "stateId"}, {"data", "state", "id"}})
}

func linearIssue(e Event) ([]string, bool) {
	if e.Subject.ID != "" {
		return []string{e.Subject.ID}, true
	}
	return firstRaw(e, [][]string{{"data", "id"}})
}

func linearAssignee(e Event) ([]string, bool) {
	return firstRaw(e, [][]string{{"data", "assigneeId"}})
}

func linearCreator(e Event) ([]string, bool) {
	if vals, ok := firstRaw(e, [][]string{{"data", "creatorId"}}); ok {
		return vals, true
	}
	if e.Actor.ProviderID != "" {
		return []string{e.Actor.ProviderID}, true
	}
	return nil, false
}

func linearMentioned(e Event) ([]string, bool) {
	vals := rawStrings(e.Raw, "data", "mentionedUserIds", "*")
	if len(vals) == 0 {
		return nil, false
	}
	return vals, true
}

func jiraProject(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "issue", "fields", "project", "key"); ok {
		return []string{s}, true
	}
	if e.Subject.Repo.Name != "" {
		return []string{e.Subject.Repo.Name}, true
	}
	return nil, false
}

func jiraStatus(e Event) ([]string, bool) {
	return firstRaw(e, [][]string{{"issue", "fields", "status", "id"}})
}

func webhookID(e Event) ([]string, bool) {
	if e.Thread.ExternalRef == "" {
		return nil, false
	}
	return []string{e.Thread.ExternalRef}, true
}

func factoryStage(e Event) ([]string, bool) {
	if s, ok := rawString(e.Raw, "stage"); ok {
		return []string{s}, true
	}
	if e.Subject.Kind != "" {
		return []string{e.Subject.Kind}, true
	}
	return nil, false
}

func rawString(raw json.RawMessage, path ...string) (string, bool) {
	vals := rawStrings(raw, path...)
	if len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

func rawStrings(raw json.RawMessage, path ...string) []string {
	nodes := descendAll(raw, path)
	var out []string
	for _, n := range nodes {
		var s string
		if err := json.Unmarshal(n, &s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func descendAll(raw json.RawMessage, path []string) []json.RawMessage {
	nodes := []json.RawMessage{raw}
	for _, seg := range path {
		var next []json.RawMessage
		for _, n := range nodes {
			next = append(next, descend(n, seg)...)
		}
		nodes = next
	}
	return nodes
}

func descend(n json.RawMessage, seg string) []json.RawMessage {
	if len(n) == 0 || string(n) == "null" {
		return nil
	}
	if seg == "*" {
		var arr []json.RawMessage
		if err := json.Unmarshal(n, &arr); err != nil {
			return nil
		}
		return arr
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(n, &obj); err != nil {
		return nil
	}
	v, ok := obj[seg]
	if !ok {
		return nil
	}
	return []json.RawMessage{v}
}
