package defsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/polds/factory/internal/definition"
)

// Source is one git definition repository.
//
// Ref is the ref to clone. ProductionRef is the ref whose tip may be
// published. An empty ProductionRef means Ref is that ref. Comparison is
// exact: "main" and "refs/heads/main" differ.
type Source struct {
	URL           string
	Ref           string
	Subdir        string
	ProductionRef string
}

// Cloner fetches url at ref into an empty directory and returns the commit
// SHA. The commit contents stay inside dest. Git transport belongs to the
// implementation; this package only decides whether the result is published.
type Cloner interface {
	Clone(ctx context.Context, url, ref, dest string) (sha string, err error)
}

// Sync clones src and, when the result may be applied, replaces dest with
// the validated definition tree.
//
// changed is true only when dest was replaced. The caller applies that tree.
// changed is false, and dest is untouched, when the cloned SHA matches the
// last SHA published to dest, when Ref is not the production ref, when
// LoadDir reports an error, or when Subdir resolves outside the clone.
// Diagnostics from that LoadDir are returned in the mismatch and
// validation-failure cases. An escaping subdir is its own error diagnostic,
// returned before LoadDir or the tree copy. Warnings do not block
// publication. An unchanged published SHA returns no diagnostics.
//
// The published SHA is recorded beside dest, not inside it, so the definition
// digest covers only the tree the caller applies.
func Sync(ctx context.Context, src Source, dest string, c Cloner) (string, bool, []definition.Diagnostic, error) {
	if c == nil {
		return "", false, nil, errors.New("defsync: cloner is required")
	}
	if src.URL == "" {
		return "", false, nil, errors.New("defsync: url is required")
	}
	if src.Ref == "" {
		return "", false, nil, errors.New("defsync: ref is required")
	}
	if err := checkSubdir(src.Subdir); err != nil {
		return "", false, nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, nil, err
	}

	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", false, nil, fmt.Errorf("defsync: %w", err)
	}
	cloneDir, err := os.MkdirTemp(parent, ".defsync-clone-*")
	if err != nil {
		return "", false, nil, fmt.Errorf("defsync: %w", err)
	}
	defer func() { _ = os.RemoveAll(cloneDir) }()

	sha, err := c.Clone(ctx, src.URL, src.Ref, cloneDir)
	if err != nil {
		return "", false, nil, fmt.Errorf("defsync: clone: %w", err)
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", false, nil, errors.New("defsync: clone returned an empty sha")
	}
	if sha == readPublishedSHA(dest) {
		return sha, false, nil, nil
	}

	treePath := subdirPath(cloneDir, src.Subdir)
	escapes, err := subdirEscapes(cloneDir, treePath)
	if err != nil {
		return sha, false, nil, err
	}
	if escapes {
		return sha, false, outsideCloneDiag(src.Subdir), nil
	}
	if err := rejectSymlinks(treePath); err != nil {
		return sha, false, nil, err
	}
	_, diags := definition.LoadDir(treePath)
	if hasErrors(diags) || !src.onProductionRef() {
		return sha, false, diags, nil
	}
	if err := publish(treePath, dest); err != nil {
		return sha, false, diags, err
	}
	if err := writePublishedSHA(dest, sha); err != nil {
		return sha, true, diags, err
	}
	return sha, true, diags, nil
}

func (s Source) onProductionRef() bool {
	return s.ProductionRef == "" || s.Ref == s.ProductionRef
}

func checkSubdir(subdir string) error {
	if subdir == "" || subdir == "." {
		return nil
	}
	cleaned := filepath.Clean(subdir)
	if filepath.IsAbs(cleaned) {
		return fmt.Errorf("defsync: subdir %q must be relative", subdir)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("defsync: subdir %q escapes the clone", subdir)
	}
	return nil
}

func subdirPath(cloneDir, subdir string) string {
	if subdir == "" || subdir == "." {
		return cloneDir
	}
	return filepath.Join(cloneDir, filepath.Clean(subdir))
}

// subdirEscapes reports whether treePath, after resolving symlinks, sits
// outside cloneDir. A missing final component still checks the deepest
// existing ancestor, so LoadDir is not asked to walk out of the clone.
func subdirEscapes(cloneDir, treePath string) (bool, error) {
	cloneReal, err := filepath.EvalSymlinks(cloneDir)
	if err != nil {
		return false, fmt.Errorf("defsync: clone path: %w", err)
	}
	cur := treePath
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return !pathWithin(cloneReal, resolved), nil
		}
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("defsync: subdir path: %w", err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false, nil
		}
		cur = parent
	}
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func outsideCloneDiag(subdir string) []definition.Diagnostic {
	file := filepath.ToSlash(subdir)
	if file == "" {
		file = "."
	}
	return []definition.Diagnostic{{
		File:     file,
		Line:     1,
		Severity: definition.SeverityError,
		Message:  fmt.Sprintf("subdir %q resolves outside the clone", subdir),
	}}
}

func hasErrors(diags []definition.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == definition.SeverityError {
			return true
		}
	}
	return false
}

func publishedSHAPath(dest string) string {
	return dest + ".defsync-sha"
}

func readPublishedSHA(dest string) string {
	body, err := os.ReadFile(publishedSHAPath(dest))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func writePublishedSHA(dest, sha string) error {
	path := publishedSHAPath(dest)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sha+"\n"), 0o644); err != nil {
		return fmt.Errorf("defsync: record sha: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("defsync: record sha: %w", err)
	}
	return nil
}
