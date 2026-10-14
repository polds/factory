package defsync

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func publish(src, dest string) error {
	parent := filepath.Dir(dest)
	stage, err := os.MkdirTemp(parent, ".defsync-publish-*")
	if err != nil {
		return fmt.Errorf("defsync: publish: %w", err)
	}
	if err := copyTree(src, stage); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("defsync: publish: %w", err)
	}

	if _, err := os.Lstat(dest); err != nil {
		if !os.IsNotExist(err) {
			_ = os.RemoveAll(stage)
			return fmt.Errorf("defsync: publish: %w", err)
		}
		if err := os.Rename(stage, dest); err != nil {
			_ = os.RemoveAll(stage)
			return fmt.Errorf("defsync: publish: %w", err)
		}
		return nil
	}

	// stage is a unique directory, so this backup path does not collide.
	backup := stage + ".bak"
	if err := os.Rename(dest, backup); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("defsync: publish: %w", err)
	}
	if err := os.Rename(stage, dest); err != nil {
		if restoreErr := os.Rename(backup, dest); restoreErr != nil {
			_ = os.RemoveAll(stage)
			return fmt.Errorf("defsync: publish: %w", errors.Join(err, restoreErr))
		}
		_ = os.RemoveAll(stage)
		return fmt.Errorf("defsync: publish: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("defsync: publish: %w", err)
	}
	return nil
}

func entryMode(path string, d fs.DirEntry) (fs.FileMode, error) {
	mode := d.Type()
	if mode != 0 {
		return mode, nil
	}
	st, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	return st.Mode(), nil
}

func rejectSymlinks(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("defsync: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("defsync: symlink %s is not allowed", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("defsync: %s is not a directory", root)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode, err := entryMode(path, d)
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			return fmt.Errorf("defsync: symlink %s is not allowed", rel)
		}
		return nil
	})
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		mode, err := entryMode(path, d)
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("defsync: symlink %s is not allowed", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !mode.IsRegular() {
			return fmt.Errorf("defsync: unsupported file %s", rel)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
