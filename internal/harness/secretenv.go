package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/polds/factory/internal/inference"
)

// secretEnvKeys are removed from the process environment across the re-exec.
// The child receives them on the pipe and keeps them only in memory.
var secretEnvKeys = []string{
	"OPENAI_API_KEY",
	"FACTORY_RUN_TOKEN",
	EnvSpendURL,
	"FACTORY_SECRET_VALUES",
	inference.ExtraHeadersEnv,
}

type secretWire struct {
	APIKey       string `json:"openai_api_key,omitempty"`
	RunToken     string `json:"run_token,omitempty"`
	SpendURL     string `json:"spend_url,omitempty"`
	SecretValues string `json:"secret_values,omitempty"`
	ExtraHeaders string `json:"extra_headers,omitempty"`
}

// PrepareProcess re-execs the harness when seat secrets are in the environment.
// The replacement environment omits them. The child reads the pipe, closes it,
// and marks every remaining descriptor close-on-exec before Run starts.
// FACTORY_HARNESS_NO_REEXEC skips the re-exec.
func PrepareProcess(argv, env []string) ([]string, error) {
	if fd := strings.TrimSpace(lookupEnv(env, EnvSecretsFD)); fd != "" {
		wire, err := readSecretFD(fd)
		if err != nil {
			return nil, err
		}
		if err := hardenProcess(); err != nil {
			return nil, err
		}
		return restoreSecrets(env, wire), nil
	}
	if strings.TrimSpace(lookupEnv(env, EnvNoReexec)) != "" || !hasSecretEnv(env) {
		return env, nil
	}
	if err := reexecClean(argv, env); err != nil {
		return nil, err
	}
	return nil, errors.New("harness: re-exec returned")
}

func hasSecretEnv(env []string) bool {
	for _, key := range secretEnvKeys {
		if strings.TrimSpace(lookupEnv(env, key)) != "" {
			return true
		}
	}
	return false
}

func reexecClean(argv, env []string) error {
	wire := secretWire{
		APIKey:       lookupEnv(env, "OPENAI_API_KEY"),
		RunToken:     lookupEnv(env, "FACTORY_RUN_TOKEN"),
		SpendURL:     lookupEnv(env, EnvSpendURL),
		SecretValues: lookupEnv(env, "FACTORY_SECRET_VALUES"),
		ExtraHeaders: lookupEnv(env, inference.ExtraHeadersEnv),
	}
	payload, err := json.Marshal(wire)
	if err != nil || len(payload) > 48*1024 {
		return errors.New("harness: secret payload")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return errors.New("harness: secret pipe")
	}
	if err := writer.Close(); err != nil {
		_ = reader.Close()
		return errors.New("harness: secret pipe")
	}
	fd := int(reader.Fd())
	if err := clearCloexec(fd); err != nil {
		_ = reader.Close()
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		_ = reader.Close()
		return err
	}
	clean := dropKeys(env, append(secretEnvKeys, EnvSecretsFD)...)
	clean = append(clean, fmt.Sprintf("%s=%d", EnvSecretsFD, fd))
	// reader stays open. Exec replaces this process, so the parent environ
	// that held the secrets does not remain for /proc to show.
	runtime.KeepAlive(reader)
	return syscall.Exec(exe, argv, clean)
}

func readSecretFD(raw string) (secretWire, error) {
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 0 {
		return secretWire{}, errors.New("harness: secret pipe")
	}
	file := os.NewFile(uintptr(fd), "secrets")
	if file == nil {
		return secretWire{}, errors.New("harness: secret pipe")
	}
	payload, err := io.ReadAll(io.LimitReader(file, 48*1024+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(payload) == 0 || len(payload) > 48*1024 {
		return secretWire{}, errors.New("harness: secret pipe")
	}
	var wire secretWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return secretWire{}, errors.New("harness: secret pipe")
	}
	return wire, nil
}

func restoreSecrets(env []string, wire secretWire) []string {
	env = dropKeys(env, append(secretEnvKeys, EnvSecretsFD)...)
	add := func(key, value string) {
		if value != "" {
			env = append(env, key+"="+value)
		}
	}
	add("OPENAI_API_KEY", wire.APIKey)
	add("FACTORY_RUN_TOKEN", wire.RunToken)
	add(EnvSpendURL, wire.SpendURL)
	add("FACTORY_SECRET_VALUES", wire.SecretValues)
	add(inference.ExtraHeadersEnv, wire.ExtraHeaders)
	return env
}

func dropKeys(env []string, keys ...string) []string {
	deny := make(map[string]bool, len(keys))
	for _, key := range keys {
		deny[key] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if deny[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func clearCloexec(fd int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFD), 0)
	if errno != 0 {
		return errors.New("harness: secret pipe")
	}
	return nil
}
