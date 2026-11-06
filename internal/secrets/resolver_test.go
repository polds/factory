package secrets_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/secrets"
)

func TestResolveAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeSecretFile(t, dir, "SCAN_GITHUB_TOKEN", "CANARY-SYNTHETIC")
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN"}, nil)

	h, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Value(); got != "CANARY-SYNTHETIC" {
		t.Fatalf("value %q", got)
	}
	if _, err := r.Resolve(context.Background(), "OPENAI_API_KEY", "inj-1"); !secrets.IsRefRejected(err) {
		t.Fatalf("want secret_ref_rejected, got %v", err)
	}
}

func TestResolveRefreshPerInjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SCAN_GITHUB_TOKEN")
	if err := os.WriteFile(path, []byte("CANARY-OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN"}, nil)

	h1, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj-1")
	if err != nil {
		t.Fatal(err)
	}
	if h1.Value() != "CANARY-OLD" {
		t.Fatalf("first %q", h1.Value())
	}
	if err := os.WriteFile(path, []byte("CANARY-NEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj-2")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Value() != "CANARY-NEW" {
		t.Fatalf("second %q", h2.Value())
	}
}

func TestResolveUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN", "DOTDOT"}, nil)
	for _, name := range []string{"../secrets/token", "DOTDOT", "..", "a/b"} {
		_, err := r.Resolve(context.Background(), name, "inj")
		if !secrets.IsRefRejected(err) {
			t.Fatalf("name %q: want secret_ref_rejected, got %v", name, err)
		}
	}
}

func TestResolveSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "SCAN_GITHUB_TOKEN")
	if err := os.Symlink("real", link); err != nil {
		t.Skip("symlink not supported:", err)
	}
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN"}, nil)
	_, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj")
	if !secrets.IsRefRejected(err) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveEnvFallback(t *testing.T) {
	r := resolver(t, "", []string{"SCAN_GITHUB_TOKEN"}, map[string]string{"SCAN_GITHUB_TOKEN": "from-env"})
	h, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj")
	if err != nil {
		t.Fatal(err)
	}
	if h.Value() != "from-env" {
		t.Fatalf("got %q", h.Value())
	}
}

func TestResolveWorldWritableRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SCAN_GITHUB_TOKEN")
	if err := os.WriteFile(path, []byte("CANARY-SYNTHETIC"), 0o666); err != nil {
		t.Fatal(err)
	}
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN"}, nil)
	_, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj")
	if !secrets.IsRefRejected(err) || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveSubPathNotUsed(t *testing.T) {
	// Resolver reads basename files only; Kubernetes subPath mounts are not a rotation mechanism.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCAN_GITHUB_TOKEN"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := resolver(t, dir, []string{"SCAN_GITHUB_TOKEN"}, nil)
	h, err := r.Resolve(context.Background(), "SCAN_GITHUB_TOKEN", "inj-a")
	if err != nil || h.Value() != "v1" {
		t.Fatalf("first resolve: %v %q", err, h.Value())
	}
}

func TestHandleRedactsFmtVerbs(t *testing.T) {
	h := secrets.HandleForTest("NAME", "CANARY-SYNTHETIC")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		got := fmt.Sprintf(verb, h)
		if strings.Contains(got, "CANARY") {
			t.Fatalf("verb %s leaked: %q", verb, got)
		}
	}
}

func resolver(t *testing.T, dir string, allow []string, env map[string]string) secrets.Resolver {
	t.Helper()
	r, err := secrets.ResolverFromConfig(secrets.MountConfig{
		MountDir:  dir,
		Allowlist: allow,
		Env:       env,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeSecretFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
