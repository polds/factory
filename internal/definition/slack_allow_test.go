package definition

import (
	"path/filepath"
	"testing"
)

func TestSlackAllowedUserIDs(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      autoRespondToThreadReplies: true
      allowedUserIds:
        - U1
        - U2
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if len(tree.Factory.Integrations) != 1 || tree.Factory.Integrations[0].Slack == nil {
		t.Fatalf("integrations %+v", tree.Factory.Integrations)
	}
	slack := tree.Factory.Integrations[0].Slack
	if !slack.AutoRespondToThreadReplies || len(slack.AllowedUserIDs) != 2 || slack.AllowedUserIDs[0] != "U1" || slack.AllowedUserIDs[1] != "U2" {
		t.Fatalf("slack %+v", slack)
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      autoRespondToThreadReplies: true
`)
	tree, diags = LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if tree.Factory.Integrations[0].Slack.AllowedUserIDs != nil || len(tree.Factory.Integrations[0].Slack.AllowedUserIDs) != 0 {
		t.Fatalf("absent list %+v", tree.Factory.Integrations[0].Slack.AllowedUserIDs)
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      allowedUserIds: []
`)
	tree, diags = LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if tree.Factory.Integrations[0].Slack.AllowedUserIDs == nil || len(tree.Factory.Integrations[0].Slack.AllowedUserIDs) != 0 {
		t.Fatalf("empty list %+v", tree.Factory.Integrations[0].Slack.AllowedUserIDs)
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      allowedUserIds: [U1]
      surprise: true
`)
	_, diags = LoadDir(dir)
	if !diagHas(diags, `unknown key "surprise"`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}
