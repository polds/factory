package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/polds/factory/internal/inference"
)

const toolOutputLimit = 16 << 10

type toolResult struct {
	OK        bool
	Output    string
	RelPath   string
	Additions int
	Deletions int
	Changed   bool
}

type toolbox struct {
	workspace string
	env       []string
}

func (t toolbox) run(ctx context.Context, name, args string) toolResult {
	switch name {
	case "bash":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Command == "" {
			return toolResult{Output: "bash requires command"}
		}
		return t.bash(ctx, p.Command)
	case "read":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
			return toolResult{Output: "read requires path"}
		}
		return t.read(p.Path)
	case "write":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
			return toolResult{Output: "write requires path"}
		}
		return t.write(p.Path, p.Content)
	case "edit":
		var p struct {
			Path string `json:"path"`
			Old  string `json:"old"`
			New  string `json:"new"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" || p.Old == "" {
			return toolResult{Output: "edit requires path and old"}
		}
		return t.edit(p.Path, p.Old, p.New)
	case "grep":
		var p struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Pattern == "" {
			return toolResult{Output: "grep requires pattern"}
		}
		return t.grep(p.Pattern, p.Path)
	case "glob":
		var p struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || p.Pattern == "" {
			return toolResult{Output: "glob requires pattern"}
		}
		return t.glob(p.Pattern)
	default:
		return toolResult{Output: "unknown tool " + name}
	}
}

func (t toolbox) bash(ctx context.Context, command string) toolResult {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Dir = t.workspace
	cmd.Env = t.env
	// Own process group so an interrupt can signal the shell and every
	// child it spawned. Killing only the shell leaves orphans that still
	// hold the output pipes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := limitOutput(buf.String())
	if cctx.Err() == context.DeadlineExceeded {
		return toolResult{Output: limitOutput(out + "\ncommand timed out")}
	}
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return toolResult{Output: out}
	}
	return toolResult{OK: true, Output: out}
}

// killProcessGroup signals the command's process group. Setpgid makes the
// shell the group leader, and its children stay in that group, so one
// signal reaps the pipeline and anything it backgrounded.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (t toolbox) read(path string) toolResult {
	abs, rel, err := t.resolve(path)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	if len(b) > toolOutputLimit {
		b = b[:toolOutputLimit]
	}
	return toolResult{OK: true, Output: string(b), RelPath: rel}
}

func (t toolbox) write(path, content string) toolResult {
	abs, rel, err := t.resolve(path)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	before := readOrEmpty(abs)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return toolResult{Output: err.Error()}
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return toolResult{Output: err.Error()}
	}
	add, del := lineDelta(before, content)
	return toolResult{
		OK:        true,
		Output:    "wrote " + rel,
		RelPath:   rel,
		Additions: add,
		Deletions: del,
		Changed:   before != content,
	}
}

func (t toolbox) edit(path, old, new string) toolResult {
	abs, rel, err := t.resolve(path)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	beforeBytes, err := os.ReadFile(abs)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	before := string(beforeBytes)
	n := strings.Count(before, old)
	if n != 1 {
		return toolResult{Output: fmt.Sprintf("edit found %d occurrences of old text, want 1", n)}
	}
	after := strings.Replace(before, old, new, 1)
	if err := os.WriteFile(abs, []byte(after), 0o644); err != nil {
		return toolResult{Output: err.Error()}
	}
	add, del := lineDelta(before, after)
	return toolResult{
		OK:        true,
		Output:    "edited " + rel,
		RelPath:   rel,
		Additions: add,
		Deletions: del,
		Changed:   true,
	}
}

func (t toolbox) grep(pattern, path string) toolResult {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return toolResult{Output: err.Error()}
	}
	root := t.workspace
	if path != "" {
		abs, _, err := t.resolve(path)
		if err != nil {
			return toolResult{Output: err.Error()}
		}
		root = abs
	}
	var lines []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if len(lines) >= 100 {
			return io.EOF
		}
		// Skip a file we cannot read. One unreadable path should not fail the search.
		if b, err := os.ReadFile(p); err == nil {
			rel, _ := filepath.Rel(t.workspace, p)
			rel = filepath.ToSlash(rel)
			for i, line := range strings.Split(string(b), "\n") {
				if re.MatchString(line) {
					lines = append(lines, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
					if len(lines) >= 100 {
						break
					}
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return toolResult{Output: err.Error()}
	}
	return toolResult{OK: true, Output: strings.Join(lines, "\n")}
}

func (t toolbox) glob(pattern string) toolResult {
	var matches []string
	err := filepath.WalkDir(t.workspace, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(t.workspace, p)
		rel = filepath.ToSlash(rel)
		if matchGlob(pattern, rel) {
			matches = append(matches, rel)
		}
		if len(matches) >= 200 {
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return toolResult{Output: err.Error()}
	}
	return toolResult{OK: true, Output: strings.Join(matches, "\n")}
}

func (t toolbox) resolve(path string) (abs, rel string, err error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.workspace, path)
	}
	path = filepath.Clean(path)
	rel, err = filepath.Rel(t.workspace, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %s is outside the workspace", path)
	}
	return path, filepath.ToSlash(rel), nil
}

func readOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func lineDelta(before, after string) (add, del int) {
	bc := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		bc[l]++
	}
	ac := map[string]int{}
	for _, l := range strings.Split(after, "\n") {
		ac[l]++
	}
	for l, n := range ac {
		if n > bc[l] {
			add += n - bc[l]
		}
	}
	for l, n := range bc {
		if n > ac[l] {
			del += n - ac[l]
		}
	}
	return add, del
}

func limitOutput(s string) string {
	if len(s) > toolOutputLimit {
		return s[:toolOutputLimit]
	}
	return s
}

func matchGlob(pattern, name string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if matchParts(pat[1:], name) {
				return true
			}
			if len(name) == 0 {
				return false
			}
			name = name[1:]
			continue
		}
		if len(name) == 0 {
			return false
		}
		ok, err := pathMatch(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	return len(name) == 0
}

func pathMatch(pattern, name string) (bool, error) {
	return filepath.Match(pattern, name)
}

func scrubEnv(parent, drop []string) []string {
	deny := map[string]bool{
		"OPENAI_API_KEY":          true,
		"ANTHROPIC_API_KEY":       true,
		"FACTORY_SECRET_VALUES":   true,
		inference.ExtraHeadersEnv: true,
	}
	for _, k := range drop {
		deny[k] = true
	}
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		if deny[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func lookupEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}

func secretList(task TaskFile, env []string) []string {
	var s []string
	s = append(s, task.Secrets...)
	if raw := lookupEnv(env, "FACTORY_SECRET_VALUES"); raw != "" {
		for _, line := range strings.Split(raw, "\n") {
			if line != "" {
				s = append(s, line)
			}
		}
	}
	return s
}
