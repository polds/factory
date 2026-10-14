package defsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestSyncPublishesCleanProductionTip(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	c := &fakeCloner{resps: []cloneResp{{
		sha: "sha-clean",
		files: map[string]string{
			"factory.yaml": validFactory("alpha"),
			"keep/me.txt":  "nested",
		},
	}}}
	// An empty ProductionRef means Ref is the production ref.
	src := Source{URL: "https://example.test/defs.git", Ref: "main"}

	sha, changed, diags, err := Sync(context.Background(), src, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-clean" || !changed {
		t.Fatalf("sha=%q changed=%v", sha, changed)
	}
	if msgs := errorMessages(diags); len(msgs) != 0 {
		t.Fatalf("errors: %v", msgs)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
		t.Fatalf("factory.yaml:\n%s", got)
	}
	if got := readFile(t, filepath.Join(dest, "keep", "me.txt")); got != "nested" {
		t.Fatalf("nested file %q", got)
	}
	tree, loaded := definition.LoadDir(dest)
	if tree == nil || tree.Factory.Name != "alpha" {
		t.Fatalf("tree=%v diags=%v", tree, loaded)
	}
	if errorMessages(loaded) != nil {
		t.Fatalf("published tree has errors: %v", errorMessages(loaded))
	}
	if c.urls[0] != src.URL || c.refs[0] != "main" {
		t.Fatalf("clone url=%q ref=%q", c.urls[0], c.refs[0])
	}
	if c.dests[0] == dest {
		t.Fatal("cloned directly into the published tree")
	}
	if _, err := os.Stat(c.dests[0]); !os.IsNotExist(err) {
		t.Fatalf("clone dir leaked: %v", err)
	}
	assertNoStaging(t, root)
}

func TestSyncNoopWhenSHAUnchanged(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	c := &fakeCloner{resps: []cloneResp{
		{sha: "sha-same", files: map[string]string{"factory.yaml": validFactory("alpha")}},
		// Same commit id with different bytes must not be republished.
		// Git SHAs address content; a repeat poll is a no-op.
		{sha: "sha-same", files: map[string]string{"factory.yaml": validFactory("beta")}},
	}}
	src := Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"}

	if _, changed, _, err := Sync(context.Background(), src, dest, c); err != nil || !changed {
		t.Fatalf("first sync changed=%v err=%v", changed, err)
	}
	sha, changed, diags, err := Sync(context.Background(), src, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-same" || changed {
		t.Fatalf("sha=%q changed=%v, want unchanged no-op", sha, changed)
	}
	if diags != nil {
		t.Fatalf("no-op diags=%v", diags)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
		t.Fatalf("dest rewritten:\n%s", got)
	}
	if c.calls() != 2 {
		t.Fatalf("clone calls=%d", c.calls())
	}
	assertNoStaging(t, root)
}

func TestSyncValidationFailureDoesNotApply(t *testing.T) {
	t.Run("keeps last good tree", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "def")
		c := &fakeCloner{resps: []cloneResp{
			{sha: "sha-good", files: map[string]string{"factory.yaml": validFactory("alpha")}},
			{sha: "sha-bad", files: map[string]string{
				"factory.yaml": invalidFactory(),
				"LEAK.txt":     "leaked",
			}},
		}}
		src := Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"}

		if _, _, _, err := Sync(context.Background(), src, dest, c); err != nil {
			t.Fatal(err)
		}
		sha, changed, diags, err := Sync(context.Background(), src, dest, c)
		if err != nil {
			t.Fatal(err)
		}
		if sha != "sha-bad" || changed {
			t.Fatalf("sha=%q changed=%v", sha, changed)
		}
		if len(errorMessages(diags)) == 0 {
			t.Fatal("expected validation errors")
		}
		if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
			t.Fatalf("last good tree replaced:\n%s", got)
		}
		if _, err := os.Stat(filepath.Join(dest, "LEAK.txt")); !os.IsNotExist(err) {
			t.Fatalf("partial file visible: %v", err)
		}
		assertNoStaging(t, root)
	})

	t.Run("does not create dest", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "def")
		c := &fakeCloner{resps: []cloneResp{{
			sha:   "sha-bad",
			files: map[string]string{"factory.yaml": invalidFactory()},
		}}}
		src := Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"}

		_, changed, diags, err := Sync(context.Background(), src, dest, c)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatal("changed")
		}
		if len(errorMessages(diags)) == 0 {
			t.Fatal("expected validation errors")
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("invalid tree created at dest: %v", err)
		}
		assertNoStaging(t, root)
	})
}

func TestSyncSubdirRespected(t *testing.T) {
	t.Run("publishes the subdirectory", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "def")
		// The repo root is invalid. Publishing it would fail validation.
		// Only team/factory is a definition tree.
		c := &fakeCloner{resps: []cloneResp{{
			sha: "sha-sub",
			files: map[string]string{
				"factory.yaml":              invalidFactory(),
				"README.md":                 "not the definition",
				"team/factory/factory.yaml": validFactory("from-subdir"),
				"team/factory/keep/me.txt":  "inside",
			},
		}}}
		src := Source{
			URL:           "https://example.test/defs.git",
			Ref:           "main",
			Subdir:        "team/factory",
			ProductionRef: "main",
		}

		sha, changed, diags, err := Sync(context.Background(), src, dest, c)
		if err != nil {
			t.Fatal(err)
		}
		if sha != "sha-sub" || !changed {
			t.Fatalf("sha=%q changed=%v diags=%v", sha, changed, diags)
		}
		if len(errorMessages(diags)) != 0 {
			t.Fatalf("errors: %v", errorMessages(diags))
		}
		if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("from-subdir") {
			t.Fatalf("factory.yaml:\n%s", got)
		}
		if got := readFile(t, filepath.Join(dest, "keep", "me.txt")); got != "inside" {
			t.Fatalf("nested=%q", got)
		}
		if _, err := os.Stat(filepath.Join(dest, "README.md")); !os.IsNotExist(err) {
			t.Fatal("repo root file published")
		}
		tree, _ := definition.LoadDir(dest)
		if tree == nil || tree.Factory.Name != "from-subdir" {
			t.Fatalf("tree=%v", tree)
		}
	})

	t.Run("missing subdirectory is not applied", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "def")
		c := &fakeCloner{resps: []cloneResp{{
			sha:   "sha-miss",
			files: map[string]string{"factory.yaml": validFactory("root")},
		}}}
		src := Source{
			URL:           "https://example.test/defs.git",
			Ref:           "main",
			Subdir:        "missing",
			ProductionRef: "main",
		}
		_, changed, diags, err := Sync(context.Background(), src, dest, c)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatal("changed")
		}
		if len(errorMessages(diags)) == 0 {
			t.Fatal("expected diagnostics for missing subdir")
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("dest created: %v", err)
		}
		assertNoStaging(t, root)
	})

	t.Run("rejects a subdir that escapes the clone", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "def")
		c := &fakeCloner{resps: []cloneResp{{
			sha:   "sha-escape",
			files: map[string]string{"factory.yaml": validFactory("nope")},
		}}}
		_, changed, _, err := Sync(context.Background(), Source{
			URL:           "https://example.test/defs.git",
			Ref:           "main",
			Subdir:        "../outside",
			ProductionRef: "main",
		}, dest, c)
		if err == nil {
			t.Fatal("expected error")
		}
		if changed {
			t.Fatal("changed")
		}
		if c.calls() != 0 {
			t.Fatalf("clone calls=%d", c.calls())
		}
	})
}

// TestSyncProductionRefMismatchNotApplied documents the publish rule:
// a clone whose Ref is not ProductionRef is validated and not published.
// changed is false, so the caller must not apply it. The last production
// tree stays as it was.
func TestSyncProductionRefMismatchNotApplied(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	c := &fakeCloner{resps: []cloneResp{
		{sha: "sha-main", files: map[string]string{"factory.yaml": validFactory("alpha")}},
		{sha: "sha-feature", files: map[string]string{"factory.yaml": validFactory("beta")}},
	}}
	base := Source{URL: "https://example.test/defs.git", ProductionRef: "main"}

	if _, changed, _, err := Sync(context.Background(), Source{
		URL: base.URL, Ref: "main", ProductionRef: "main",
	}, dest, c); err != nil || !changed {
		t.Fatalf("production sync changed=%v err=%v", changed, err)
	}

	feature := base
	feature.Ref = "feature"
	sha, changed, diags, err := Sync(context.Background(), feature, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-feature" || changed {
		t.Fatalf("sha=%q changed=%v, mismatch must not apply", sha, changed)
	}
	if len(errorMessages(diags)) != 0 {
		t.Fatalf("valid non-production ref returned errors: %v", errorMessages(diags))
	}
	if c.refs[1] != "feature" {
		t.Fatalf("cloned ref %q", c.refs[1])
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
		t.Fatalf("non-production ref replaced dest:\n%s", got)
	}
	assertNoStaging(t, root)
}

func TestSyncWarningsDoNotBlockPublish(t *testing.T) {
	// Review and implement share one model. T2 warns on that pair and no
	// longer warns that pipeline is "not applied in M2". Publication follows
	// error severity only, so the warning is returned and the tree is published.
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	files := sameModelFactory()
	preview := filepath.Join(root, "preview")
	writeTree(t, preview, files)
	_, loaded := definition.LoadDir(preview)
	if msgs := errorMessages(loaded); len(msgs) != 0 {
		t.Fatalf("fixture errors: %v", msgs)
	}

	c := &fakeCloner{resps: []cloneResp{{sha: "sha-warn", files: files}}}
	sha, changed, diags, err := Sync(context.Background(), Source{
		URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main",
	}, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-warn" || !changed {
		t.Fatalf("sha=%q changed=%v diags=%v", sha, changed, diags)
	}
	if msgs := errorMessages(diags); len(msgs) != 0 {
		t.Fatalf("errors: %v", msgs)
	}
	if hasWarning(loaded, "same model") && !hasWarning(diags, "same model") {
		t.Fatalf("dropped same-model warning: sync=%v load=%v", diags, loaded)
	}
	if _, err := os.Stat(filepath.Join(dest, "agents", "reviewer", "agent.md")); err != nil {
		t.Fatal(err)
	}
	assertNoStaging(t, root)
}

func TestSyncSubdirSymlinkDoesNotPublishOutsideClone(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	nested := filepath.Join(outside, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "factory.yaml"), []byte(validFactory("escaped")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SECRET.txt"), []byte("host-secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(root, "def")
	c := clonerFunc(func(clone string) (string, error) {
		if err := os.Symlink(outside, filepath.Join(clone, "link")); err != nil {
			return "", err
		}
		return "sha-escape", nil
	})
	sha, changed, diags, err := Sync(context.Background(), Source{
		URL:           "https://example.test/defs.git",
		Ref:           "main",
		Subdir:        "link/nested",
		ProductionRef: "main",
	}, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-escape" || changed {
		t.Fatalf("sha=%q changed=%v", sha, changed)
	}
	if !hasError(diags, "outside") {
		t.Fatalf("diags=%v", diags)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SECRET.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file published: %v", err)
	}
	assertNoStaging(t, root)
}

func TestSyncRecordsSHAWhenBackupCleanupFails(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	c := &fakeCloner{resps: []cloneResp{
		{sha: "sha-1", files: map[string]string{"factory.yaml": validFactory("alpha")}},
		{sha: "sha-2", files: map[string]string{"factory.yaml": validFactory("beta")}},
		{sha: "sha-2", files: map[string]string{"factory.yaml": validFactory("gamma")}},
	}}
	src := Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"}
	if _, changed, _, err := Sync(context.Background(), src, dest, c); err != nil || !changed {
		t.Fatalf("first sync changed=%v err=%v", changed, err)
	}

	prev := removeTree
	removeTree = func(string) error { return errors.New("backup busy") }
	t.Cleanup(func() { removeTree = prev })

	sha, changed, _, err := Sync(context.Background(), src, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-2" || !changed {
		t.Fatalf("sha=%q changed=%v", sha, changed)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("beta") {
		t.Fatalf("dest:\n%s", got)
	}
	if got := readPublishedSHA(dest); got != "sha-2" {
		t.Fatalf("recorded sha %q", got)
	}

	sha, changed, _, err = Sync(context.Background(), src, dest, c)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "sha-2" || changed {
		t.Fatalf("repeat sha=%q changed=%v", sha, changed)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("beta") {
		t.Fatalf("same sha republished:\n%s", got)
	}
}

func TestSyncCloneErrorLeavesDestUntouched(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	boom := errors.New("transport down")
	c := &fakeCloner{resps: []cloneResp{
		{sha: "sha-good", files: map[string]string{"factory.yaml": validFactory("alpha")}},
		{err: boom},
	}}
	src := Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"}
	if _, _, _, err := Sync(context.Background(), src, dest, c); err != nil {
		t.Fatal(err)
	}
	sha, changed, diags, err := Sync(context.Background(), src, dest, c)
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if sha != "" || changed || diags != nil {
		t.Fatalf("sha=%q changed=%v diags=%v", sha, changed, diags)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
		t.Fatalf("dest changed:\n%s", got)
	}
	assertNoStaging(t, root)
}

func TestSyncHonorsCanceledContext(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "def")
	c := &fakeCloner{resps: []cloneResp{{
		sha:   "sha-1",
		files: map[string]string{"factory.yaml": validFactory("alpha")},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, changed, _, err := Sync(ctx, Source{
		URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main",
	}, dest, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if changed {
		t.Fatal("changed")
	}
	if c.calls() != 0 {
		t.Fatalf("clone calls=%d", c.calls())
	}
}

func TestSyncRejectsSymlinkInTree(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "def")
	outside := filepath.Join(root, "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := clonerFunc(func(dest string) (string, error) {
		if err := os.WriteFile(filepath.Join(dest, "factory.yaml"), []byte(validFactory("alpha")), 0o644); err != nil {
			return "", err
		}
		if err := os.Symlink(outside, filepath.Join(dest, "secret")); err != nil {
			return "", err
		}
		return "sha-link", nil
	})
	_, changed, _, err := Sync(context.Background(), Source{
		URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main",
	}, dest, c)
	if err == nil {
		t.Fatal("expected symlink error")
	}
	if changed {
		t.Fatal("changed")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest created: %v", statErr)
	}
	assertNoStaging(t, root)
}

type cloneResp struct {
	sha   string
	files map[string]string
	err   error
}

type fakeCloner struct {
	mu    sync.Mutex
	resps []cloneResp
	i     int
	urls  []string
	refs  []string
	dests []string
}

func (f *fakeCloner) Clone(ctx context.Context, url, ref, dest string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	f.refs = append(f.refs, ref)
	f.dests = append(f.dests, dest)
	var resp cloneResp
	switch {
	case len(f.resps) == 0:
		return "", errors.New("no clone response")
	case f.i >= len(f.resps):
		resp = f.resps[len(f.resps)-1]
	default:
		resp = f.resps[f.i]
		f.i++
	}
	if resp.err != nil {
		return "", resp.err
	}
	for name, body := range resp.files {
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return resp.sha, nil
}

func (f *fakeCloner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dests)
}

type clonerFunc func(dest string) (string, error)

func (f clonerFunc) Clone(ctx context.Context, _, _ string, dest string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f(dest)
}

func validFactory(name string) string {
	return "schemaVersion: v1alpha1\nname: " + name + "\n"
}

func sameModelFactory() map[string]string {
	return map[string]string{
		"factory.yaml":                "schemaVersion: v1alpha1\nname: warned\nagentDefaults:\n  runner: local\n",
		"runners/local.yaml":          "description: Local docker runner\nplatform:\n  os: linux\n  arch: x86_64\n  linux:\n    dockerImage: factory-m1-task:dev\n",
		"agents/implementer/agent.md": agentFile("IMPLEMENT", "claude-5-opus-high", "Implement"),
		"agents/reviewer/agent.md":    agentFile("REVIEW", "claude-5-opus-high", "Review"),
	}
}

func agentFile(kind, model, title string) string {
	return "---\ndescription: " + title + ".\nagentType: " + kind + "\nmodel: " + model + "\n---\n\n# " + title + "\n"
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hasWarning(diags []definition.Diagnostic, text string) bool {
	for _, d := range diags {
		if d.Severity == definition.SeverityWarning && strings.Contains(d.Message, text) {
			return true
		}
	}
	return false
}

func hasError(diags []definition.Diagnostic, text string) bool {
	for _, d := range diags {
		if d.Severity == definition.SeverityError && strings.Contains(d.Message, text) {
			return true
		}
	}
	return false
}

func invalidFactory() string {
	return "schemaVersion: v1alpha1\nname: bad\nnotAKey: true\n"
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func errorMessages(diags []definition.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if d.Severity == definition.SeverityError {
			out = append(out, d.File+": "+d.Message)
		}
	}
	return out
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".defsync") {
			t.Errorf("left staging entry %s", e.Name())
		}
	}
}
