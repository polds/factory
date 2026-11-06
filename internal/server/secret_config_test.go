package server

import (
	"testing"

	"github.com/polds/factory/internal/secrets"
)

func TestParseSecretMountSettings(t *testing.T) {
	got := ParseSecretMountSettings(func(k string) string {
		switch k {
		case "FACTORY_SECRET_MOUNT_DIR":
			return "/var/run/factory/secrets"
		case "FACTORY_SECRET_ALLOWLIST":
			return " SCAN_GITHUB_TOKEN , NPM_TOKEN "
		default:
			return ""
		}
	})
	if got.MountDir != "/var/run/factory/secrets" {
		t.Fatalf("dir %q", got.MountDir)
	}
	if len(got.Allowlist) != 2 || got.Allowlist[0] != "SCAN_GITHUB_TOKEN" {
		t.Fatalf("allow %v", got.Allowlist)
	}
}

func TestBuildResolverFromEnvSecrets(t *testing.T) {
	dir := t.TempDir()
	settings := SecretMountSettings{
		MountDir:  dir,
		Allowlist: []string{"SCAN_GITHUB_TOKEN"},
	}
	res, err := settings.BuildResolver(map[string]string{"SCAN_GITHUB_TOKEN": "CANARY-SYNTHETIC"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("expected resolver")
	}
	h, err := res.Resolve(t.Context(), "SCAN_GITHUB_TOKEN", "inj")
	if err != nil {
		t.Fatal(err)
	}
	if h.Value() != "CANARY-SYNTHETIC" {
		t.Fatalf("got %q", h.Value())
	}
}

func TestBuildResolverNilWhenUnset(t *testing.T) {
	res, err := (SecretMountSettings{}).BuildResolver(nil)
	if err != nil || res != nil {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestSecretNamesDedupes(t *testing.T) {
	got := secretNames([]string{"A", "B"}, []string{"B", "C"})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestGatewayStubUnavailable(t *testing.T) {
	var stub secrets.FailClosedAllocator
	if stub.GuaranteesCeiling() {
		t.Fatal("must not claim ceiling guarantee")
	}
}
