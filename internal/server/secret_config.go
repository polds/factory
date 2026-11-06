package server

import (
	"fmt"
	"strings"

	"github.com/polds/factory/internal/secrets"
)

// SecretMountSettings configures the secrets.Resolver mount seam for factoryd.
// Production injection wiring is owned by M9-H; this only parses and validates config.
type SecretMountSettings struct {
	MountDir  string
	Allowlist []string
}

// ParseSecretMountSettings reads FACTORY_SECRET_MOUNT_DIR and FACTORY_SECRET_ALLOWLIST.
// Allowlist entries are comma-separated secret names. Empty allowlist means "inherit none"
// and ResolverFromConfig will fail until names are configured.
func ParseSecretMountSettings(getenv func(string) string) SecretMountSettings {
	raw := strings.TrimSpace(getenv("FACTORY_SECRET_ALLOWLIST"))
	var allow []string
	if raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				allow = append(allow, part)
			}
		}
	}
	return SecretMountSettings{
		MountDir:  strings.TrimSpace(getenv("FACTORY_SECRET_MOUNT_DIR")),
		Allowlist: allow,
	}
}

// BuildResolver constructs a Resolver when mount settings and env secrets are present.
// Returns nil,nil when FACTORY_SECRET_MOUNT_DIR is unset (legacy env-only mode).
func (s SecretMountSettings) BuildResolver(envSecrets map[string]string) (secrets.Resolver, error) {
	if strings.TrimSpace(s.MountDir) == "" && len(s.Allowlist) == 0 {
		return nil, nil
	}
	cfg := secrets.MountConfig{
		MountDir:  s.MountDir,
		Allowlist: s.Allowlist,
		Env:       envSecrets,
	}
	if len(cfg.Allowlist) == 0 {
		for name := range envSecrets {
			cfg.Allowlist = append(cfg.Allowlist, name)
		}
	}
	res, err := secrets.ResolverFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("secret mount: %w", err)
	}
	return res, nil
}
