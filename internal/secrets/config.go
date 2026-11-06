package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MountConfig locates allowlisted secrets as files under MountDir and optional env fallbacks.
type MountConfig struct {
	// MountDir holds one file per allowlisted name. Each injection re-reads the file.
	MountDir string
	// Allowlist is the only set of names Resolve accepts. Empty rejects every name.
	Allowlist []string
	// Env maps secret name to process env value for compatibility when no file exists.
	Env map[string]string
}

// Validate checks mount configuration without reading secret material.
func (c MountConfig) Validate() error {
	if strings.TrimSpace(c.MountDir) == "" && len(c.Env) == 0 {
		return fmt.Errorf("secrets: mount dir or env fallback required")
	}
	if len(c.Allowlist) == 0 {
		return fmt.Errorf("secrets: allowlist is required")
	}
	if dir := strings.TrimSpace(c.MountDir); dir != "" {
		st, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("secrets: mount dir: %w", err)
		}
		if !st.IsDir() {
			return fmt.Errorf("secrets: mount dir is not a directory")
		}
		if err := rejectWritableDir(dir); err != nil {
			return err
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("secrets: mount dir: %w", err)
		}
		clean := filepath.Clean(abs)
		if clean != abs {
			return fmt.Errorf("secrets: mount dir path is not clean")
		}
	}
	return nil
}

// ResolverFromConfig builds a Resolver from mount configuration.
func ResolverFromConfig(c MountConfig) (*MountResolver, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	allow := make(map[string]struct{}, len(c.Allowlist))
	for _, name := range c.Allowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allow[name] = struct{}{}
	}
	if len(allow) == 0 {
		return nil, fmt.Errorf("secrets: allowlist is required")
	}
	var mountRoot string
	if dir := strings.TrimSpace(c.MountDir); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		mountRoot = filepath.Clean(abs)
	}
	env := map[string]string{}
	for k, v := range c.Env {
		if _, ok := allow[k]; ok && v != "" {
			env[k] = v
		}
	}
	return &MountResolver{
		allow:     allow,
		mountRoot: mountRoot,
		env:       env,
	}, nil
}
