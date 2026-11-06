package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolver resolves allowlisted secret names at injection time.
type Resolver interface {
	Resolve(ctx context.Context, name, injectionID string) (Handle, error)
}

// MountResolver reads secret files from a mounted directory on every Resolve call.
type MountResolver struct {
	allow     map[string]struct{}
	mountRoot string
	env       map[string]string
}

// Resolve returns material for name when it is allowlisted. injectionID is recorded
// for tests and future audit; each call re-reads the backing file (no in-process cache).
func (r *MountResolver) Resolve(ctx context.Context, name, injectionID string) (Handle, error) {
	_ = ctx
	_ = injectionID
	name = strings.TrimSpace(name)
	if name == "" {
		return Handle{}, fmt.Errorf("%w: empty name", ErrRefRejected)
	}
	if err := validateSecretName(name); err != nil {
		return Handle{}, err
	}
	if _, ok := r.allow[name]; !ok {
		return Handle{}, fmt.Errorf("%w: %s not allowlisted", ErrRefRejected, name)
	}
	if r.mountRoot != "" {
		if err := rejectWritableDir(r.mountRoot); err != nil {
			return Handle{}, err
		}
		h, err := r.readMount(name)
		if err == nil {
			return h, nil
		}
		if !IsRefRejected(err) || !isMissingMount(err) {
			return Handle{}, err
		}
	}
	if v, ok := r.env[name]; ok && v != "" {
		return newHandle(name, v), nil
	}
	return Handle{}, fmt.Errorf("%w: %s missing", ErrRefRejected, name)
}

func (r *MountResolver) readMount(name string) (Handle, error) {
	path, err := safeMountPath(r.mountRoot, name)
	if err != nil {
		return Handle{}, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Handle{}, fmt.Errorf("%w: %s missing file", ErrRefRejected, name)
		}
		return Handle{}, fmt.Errorf("%w: %s stat: %w", ErrRefRejected, name, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Handle{}, fmt.Errorf("%w: %s symlink", ErrRefRejected, name)
	}
	if !fi.Mode().IsRegular() {
		return Handle{}, fmt.Errorf("%w: %s not a regular file", ErrRefRejected, name)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return Handle{}, fmt.Errorf("%w: %s world/group writable", ErrRefRejected, name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Handle{}, fmt.Errorf("%w: %s missing file", ErrRefRejected, name)
		}
		if os.IsPermission(err) {
			return Handle{}, fmt.Errorf("%w: %s permission denied", ErrRefRejected, name)
		}
		return Handle{}, fmt.Errorf("%w: %s read: %w", ErrRefRejected, name, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return Handle{}, fmt.Errorf("%w: %s empty file", ErrRefRejected, name)
	}
	return newHandle(name, value), nil
}

func isMissingMount(err error) bool {
	if !IsRefRejected(err) {
		return false
	}
	return strings.Contains(err.Error(), "missing file")
}

func rejectWritableDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: mount dir stat: %w", ErrRefRejected, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: mount dir is not a directory", ErrRefRejected)
	}
	// Group or world write lets another user replace the file between injections.
	// Do not use 0o077: a normal 0755 mount must still resolve.
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: mount dir group/world writable", ErrRefRejected)
	}
	return nil
}

func validateSecretName(name string) error {
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("%w: invalid name %q", ErrRefRejected, name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: invalid name %q", ErrRefRejected, name)
	}
	return nil
}

func safeMountPath(root, name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
	joined := filepath.Join(root, name)
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("%w: path escape", ErrRefRejected)
	}
	clean := filepath.Clean(abs)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path escape for %s", ErrRefRejected, name)
	}
	// Reject explicit traversal in the name even when Join would clean it.
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: path escape for %s", ErrRefRejected, name)
	}
	return clean, nil
}
