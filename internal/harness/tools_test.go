package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestToolsWriteEditGrepGlobAndJail(t *testing.T) {
	ws := t.TempDir()
	box := toolbox{workspace: ws, env: []string{"FACTORY_VISIBLE=yes"}}
	ctx := context.Background()

	wrote := box.run(ctx, "write", `{"path":"a/b/note.txt","content":"hello\n"}`)
	if !wrote.OK || wrote.RelPath != "a/b/note.txt" || wrote.Additions != 1 || wrote.Output != "wrote a/b/note.txt" {
		t.Fatalf("%+v", wrote)
	}
	body, err := os.ReadFile(filepath.Join(ws, "a/b/note.txt"))
	if err != nil || string(body) != "hello\n" {
		t.Fatalf("file %q err %v", body, err)
	}

	edited := box.run(ctx, "edit", `{"path":"a/b/note.txt","old":"hello","new":"hello world"}`)
	if !edited.OK || edited.Output != "edited a/b/note.txt" {
		t.Fatalf("%+v", edited)
	}
	body, err = os.ReadFile(filepath.Join(ws, "a/b/note.txt"))
	if err != nil || string(body) != "hello world\n" {
		t.Fatalf("edited %q err %v", body, err)
	}

	found := box.run(ctx, "grep", `{"pattern":"world","path":"a"}`)
	if !found.OK || found.Output != "a/b/note.txt:1:hello world" {
		t.Fatalf("%q", found.Output)
	}
	listed := box.run(ctx, "glob", `{"pattern":"**/*.txt"}`)
	if !listed.OK || listed.Output != "a/b/note.txt" {
		t.Fatalf("%q", listed.Output)
	}

	outside := box.run(ctx, "write", `{"path":"/etc/factory-harness-should-not-exist","content":"nope"}`)
	if outside.OK || outside.Output != "path /etc/factory-harness-should-not-exist is outside the workspace" {
		t.Fatalf("%+v", outside)
	}
	if _, err := os.Stat("/etc/factory-harness-should-not-exist"); !os.IsNotExist(err) {
		t.Fatalf("outside file err %v", err)
	}

	bash := box.run(ctx, "bash", `{"command":"printf %s \"$FACTORY_VISIBLE\""}`)
	if !bash.OK || bash.Output != "yes" {
		t.Fatalf("%+v", bash)
	}
}
