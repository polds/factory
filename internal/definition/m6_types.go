package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// BenchmarkSuite is benchmarks/<suite>/suite.yaml. T57 loads and validates it.
type BenchmarkSuite struct {
	Name           string                   `json:"name,omitempty"`
	Description    string                   `json:"description,omitempty"`
	Agent          string                   `json:"agent,omitempty"`
	Configurations []BenchmarkConfiguration `json:"configurations,omitempty"`
	Tasks          []string                 `json:"tasks,omitempty"`
}

// BenchmarkConfiguration is one suite configuration. DefinitionRef is the
// optional D3 pointer at another definition tree. An empty value means the
// factory's own tree.
type BenchmarkConfiguration struct {
	Name          string             `json:"name,omitempty"`
	Agents        []BenchmarkBinding `json:"agents,omitempty"`
	DefinitionRef string             `json:"definitionRef,omitempty"`
}

// BenchmarkBinding is one agent binding inside a configuration.
// Model and Harness are the existing definition fields. A binding sets one
// of them.
type BenchmarkBinding struct {
	Agent   string   `json:"agent,omitempty"`
	Model   string   `json:"model,omitempty"`
	Harness *Harness `json:"harness,omitempty"`
}

// BenchmarkTask is benchmarks/<suite>/tasks/<task>.yaml.
type BenchmarkTask struct {
	Name             string    `json:"name,omitempty"`
	Title            string    `json:"title,omitempty"`
	Prompt           string    `json:"prompt,omitempty"`
	SuccessCriteria  string    `json:"successCriteria,omitempty"`
	SourceRunID      string    `json:"sourceRunId,omitempty"`
	Tags             []string  `json:"tags,omitempty"`
	StartingRepoRefs []RepoRef `json:"startingRepoRefs,omitempty"`
}

// RepoRef is the docs/03 startingRepoRefs token host:owner/repo@<40-hex>.
type RepoRef struct {
	Host  string
	Owner string
	Repo  string
	SHA   string
}

// String returns the docs/03 token.
func (r RepoRef) String() string {
	return r.Host + ":" + r.Owner + "/" + r.Repo + "@" + r.SHA
}

// ParseRepoRef parses host:owner/repo@<40 lowercase hex>.
func ParseRepoRef(s string) (RepoRef, error) {
	s = strings.TrimSpace(s)
	host, rest, ok := strings.Cut(s, ":")
	if !ok || host == "" || strings.Contains(host, "/") {
		return RepoRef{}, fmt.Errorf("repo ref %q is not host:owner/repo@sha", s)
	}
	ownerRepo, sha, ok := strings.Cut(rest, "@")
	if !ok {
		return RepoRef{}, fmt.Errorf("repo ref %q is not host:owner/repo@sha", s)
	}
	owner, repo, ok := strings.Cut(ownerRepo, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return RepoRef{}, fmt.Errorf("repo ref %q is not host:owner/repo@sha", s)
	}
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return RepoRef{}, fmt.Errorf("repo ref %q sha is not 40 hex characters", s)
	}
	return RepoRef{Host: host, Owner: owner, Repo: repo, SHA: sha}, nil
}

// UnmarshalJSON accepts the docs/03 string or the host/owner/repo/sha object.
func (r *RepoRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		parsed, err := ParseRepoRef(s)
		if err != nil {
			return err
		}
		*r = parsed
		return nil
	}
	var obj struct {
		Host  string `json:"host"`
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		SHA   string `json:"sha"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	parsed, err := ParseRepoRef(obj.Host + ":" + obj.Owner + "/" + obj.Repo + "@" + obj.SHA)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// MarshalJSON emits the docs/03 string.
func (r RepoRef) MarshalJSON() ([]byte, error) {
	if r.Host == "" && r.Owner == "" && r.Repo == "" && r.SHA == "" {
		return []byte("null"), nil
	}
	return json.Marshal(r.String())
}

// WeakensApprovals reports approval gates that head weakens relative to base:
// a removed gate, required changing from true to false, approvers widened,
// or a gate renamed. T57 writes the body.
func WeakensApprovals(base, head *Tree) []Diagnostic {
	return nil
}

// GuardRails reports definition changes the improvement loop must not land.
// T57 writes the body: WeakensApprovals plus the rail-5 selfImprovement check.
func GuardRails(base, head *Tree) []Diagnostic {
	return nil
}
