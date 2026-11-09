package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// Event text for one evidence command is redacted, then cut to this cap.
	EvidenceCap = 256 << 10
	// EvidenceTruncated is the marker kept inside EvidenceCap.
	EvidenceTruncated = "\n[truncated]\n"

	EvidenceToolError            = "tool_error"
	EvidenceTestsFailed          = "tests_failed"
	EvidenceTestsPassed          = "tests_passed"
	EvidenceSkipOnly             = "skip_only"
	EvidencePgvectorUnavailable  = "pgvector_unavailable"
	EvidencePrivilegeUnavailable = "privilege_unavailable"
)

// EvidencePackagePath accepts an empty package or one relative package path.
// The value is not a shell command.
func EvidencePackagePath(pkg string) error {
	if pkg == "" || pkg == "." {
		return nil
	}
	return lexicalEvidence(strings.TrimPrefix(pkg, "./"))
}

// EvidenceMutationPath accepts an empty path or one relative file path.
func EvidenceMutationPath(rel string) error {
	if rel == "" {
		return nil
	}
	return lexicalEvidence(rel)
}

func lexicalEvidence(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) || strings.Contains(rel, "\x00") {
		return errors.New("evidence path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("evidence path")
		}
	}
	return nil
}

// runEvidence retains the named test after checkout and before any model call.
// A task file without EvidencePackage does not reach this function.
func runEvidence(ctx context.Context, opt Options, task TaskFile, log *Log, finish func(int, string, string) int) int {
	secrets := secretList(task, opt.Env)
	if task.EvidenceStatus == EvidencePrivilegeUnavailable {
		recordEvidence(log, secrets, EvidencePrivilegeUnavailable, EvidencePrivilegeUnavailable)
		return finish(ExitEnvironment, EvidencePrivilegeUnavailable, "evidence privilege_unavailable")
	}
	if dsn := lookupEnv(opt.Env, EnvTestDSN); dsn != "" {
		ok, err := vectorAvailable(ctx, dsn)
		if err != nil {
			status := EvidenceToolError
			if privilegeDenied(err) {
				status = EvidencePrivilegeUnavailable
			}
			recordEvidence(log, secrets, status, status)
			return finish(ExitEnvironment, status, "evidence "+status)
		}
		if !ok {
			recordEvidence(log, secrets, EvidencePgvectorUnavailable, EvidencePgvectorUnavailable)
			return finish(ExitEnvironment, EvidencePgvectorUnavailable, "evidence pgvector_unavailable")
		}
	}
	if task.MutationPath == "" {
		status, body := runGoTest(ctx, opt, task.EvidencePackage)
		recordEvidence(log, secrets, status, body)
		code := ExitEnvironment
		if status == EvidenceTestsPassed {
			code = ExitOK
		}
		return finish(code, status, "evidence "+status)
	}
	return runMutation(ctx, opt, task, log, finish, secrets)
}

func recordEvidence(log *Log, secrets []string, status, body string) {
	text, digest := capTranscript(Redact(body, secrets))
	_ = log.Emit(Event{
		Type:         EventEvidenceTest,
		Status:       status,
		Text:         text,
		OutputDigest: digest,
	})
}

func capTranscript(redacted string) (string, string) {
	digest := Digest(redacted)
	if len(redacted) <= EvidenceCap {
		return redacted, digest
	}
	marker := EvidenceTruncated
	keep := EvidenceCap - len(marker)
	if keep < 0 {
		keep = 0
		marker = marker[:EvidenceCap]
	}
	return redacted[:keep] + marker, digest
}

func vectorAvailable(ctx context.Context, dsn string) (bool, error) {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(probe, dsn)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(probe) }()
	var ok bool
	err = conn.QueryRow(probe, `SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector')`).Scan(&ok)
	return ok, err
}

func privilegeDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func runGoTest(ctx context.Context, opt Options, pkg string) (string, string) {
	if err := EvidencePackagePath(pkg); err != nil || pkg == "" {
		return EvidenceToolError, EvidenceToolError
	}
	bin, err := lookGo(opt.Env)
	if err != nil {
		return EvidenceToolError, "go: not found"
	}
	cmd := exec.CommandContext(ctx, bin, "test", "-json", "-count=1", pkg)
	cmd.Dir = opt.Workspace
	cmd.Env = scrubEnv(opt.Env, nil)
	if lookupEnv(cmd.Env, "GOTOOLCHAIN") == "" {
		cmd.Env = append(cmd.Env, "GOTOOLCHAIN=local")
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	body := stdout.String() + stderr.String()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return classifyGoTest(body, exit.ExitCode()), body
		}
		if errors.Is(err, exec.ErrNotFound) || isNotFound(err) {
			return EvidenceToolError, "go: not found"
		}
		return EvidenceToolError, body
	}
	return classifyGoTest(body, 0), body
}

func isNotFound(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return os.IsNotExist(pathErr.Err)
	}
	return false
}

func lookGo(env []string) (string, error) {
	path := lookupEnv(env, "PATH")
	if path == "" {
		return "", errors.New("go: not found")
	}
	for _, dir := range filepathSplit(path) {
		candidate := dir + "/go"
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("go: not found")
}

func filepathSplit(path string) []string {
	return strings.Split(path, string(os.PathListSeparator))
}

// classifyGoTest reads go test -json. A printed PASS line is not a pass.
// Zero run events, or only skips, is skip_only when the process exited 0,
// including ok with no tests. A non-zero exit without a passing test is
// tests_failed. A missing binary is classified by the caller as tool_error.
func classifyGoTest(combined string, exitCode int) string {
	runs, passes, fails := 0, 0, 0
	for _, line := range strings.Split(combined, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Action == "" || ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "run":
			runs++
		case "pass":
			passes++
		case "fail":
			fails++
		case "skip":
		}
	}
	if runs == 0 || (passes == 0 && fails == 0) {
		if exitCode != 0 {
			return EvidenceTestsFailed
		}
		return EvidenceSkipOnly
	}
	if fails > 0 || exitCode != 0 {
		return EvidenceTestsFailed
	}
	return EvidenceTestsPassed
}
