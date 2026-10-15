package definition

import (
	"path/filepath"
	"testing"
)

func TestSlackChannel(t *testing.T) {
	dir := baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      autoRespondToThreadReplies: true
      channel: C0123ABCDEF
`)
	tree, diags := LoadDir(dir)
	if len(diags) != 0 {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
	if len(tree.Factory.Integrations) != 1 || tree.Factory.Integrations[0].Slack == nil {
		t.Fatalf("integrations %+v", tree.Factory.Integrations)
	}
	slack := tree.Factory.Integrations[0].Slack
	if slack.Channel != "C0123ABCDEF" || !slack.AutoRespondToThreadReplies {
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
	if tree.Factory.Integrations[0].Slack == nil || tree.Factory.Integrations[0].Slack.Channel != "" {
		t.Fatalf("channel %+v", tree.Factory.Integrations[0].Slack)
	}

	dir = baseTree(t)
	writeFile(t, filepath.Join(dir, "factory.yaml"), baseFactory()+`
integrations:
  - type: slack
    slack:
      channel: C0123ABCDEF
      surprise: true
`)
	_, diags = LoadDir(dir)
	if !diagHas(diags, `unknown key "surprise"`) {
		t.Fatalf("diagnostics:\n%s", formatDiags(diags))
	}
}
