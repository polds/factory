//go:build linux

package dreaminglive

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// reexecCleanDriver replaces this process when it still has its original
// kernel environment. os.Unsetenv does not change /proc/<pid>/environ.
// The replacement environment is an allowlist, so a driver sentinel cannot
// remain for a seat tool that walks its ancestors.
// ReexecCleanDriver is the process entry. The binary calls it before seats.
func ReexecCleanDriver() error {
	return reexecCleanDriver()
}

func reexecCleanDriver() error {
	if os.Getenv("FACTORY_DRIVER_CLEAN") == "1" {
		return nil
	}
	// Preflight and publish hold the App key and start no seat.
	// Only the chain process re-execs onto a clean kernel environment.
	if len(os.Args) != 2 || (os.Args[1] != "chain" && os.Args[1] != "live") {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if raw := os.Getenv("FACTORY_MODEL_KEY_FD"); raw != "" {
		if fd, err := strconv.Atoi(raw); err == nil && fd >= 0 {
			_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), 0)
			if errno != 0 {
				return errno
			}
		}
	}
	env := driverAllowEnv(os.Environ())
	env = append(env, "FACTORY_DRIVER_CLEAN=1")
	return syscall.Exec(exe, os.Args, env)
}

func driverAllowEnv(src []string) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true,
		"LANG": true, "LC_ALL": true, "USER": true, "LOGNAME": true, "TERM": true, "SHELL": true,
		"GOTOOLCHAIN": true, "GOCACHE": true, "GOPATH": true, "GOROOT": true, "GOMODCACHE": true,
		"FACTORY_DRIVER_CLEAN": true, "FACTORY_MODEL_KEY_FD": true, "FACTORY_CHAIN_OUT": true,
		"FACTORY_DATABASE_URL": true, "FACTORY_ROLE_SOURCE": true, "FACTORY_SHA": true,
		"FACTORY_GIT_DIR": true, "FACTORY_REQUIRED_SHAS": true, "FACTORY_SECRET_NAMES": true,
		"FACTORY_PUBLISH_DRAFT_REPO": true, "FACTORY_GITHUB_API_BASE": true,
		"FACTORY_S3_ENDPOINT": true, "FACTORY_S3_BUCKET": true, "FACTORY_S3_ACCESS_KEY": true,
		"FACTORY_S3_SECRET_KEY": true, "FACTORY_S3_REGION": true, "FACTORY_S3_FORCE_PATH_STYLE": true,
		"FACTORY_EVIDENCE_PACKAGE": true, "FACTORY_EVIDENCE_MUTATION": true,
		"FACTORY_HTTP_ADDR": true, "FACTORY_DEFINITION_PATH": true,
		"ROLE_BLOBS": true, "DREAMING_PIN": true, "PREFLIGHT_JOB_FILE": true,
		"CONFIRM": true, "MODE": true,
		"M1_OPENAI_BASE_URL": true, "M6_GITHUB_APP_ID": true,
		"PWD": true,
	}
	var out []string
	for _, kv := range src {
		key, _, ok := strings.Cut(kv, "=")
		if ok && allow[key] {
			out = append(out, kv)
		}
	}
	return out
}
