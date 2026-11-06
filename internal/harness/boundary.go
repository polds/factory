package harness

import (
	"context"
	"strings"
)

const (
	// OutcomeIsolated means bash and MCP were exercised and the planted secret stayed inside.
	OutcomeIsolated = "isolated"
	// OutcomeLiveBlocked means the adapter did not prove the boundary.
	OutcomeLiveBlocked = "liveBlocked"

	// EvidenceAdapter is an adapter-level bash and MCP probe.
	EvidenceAdapter = "adapter"
	// EvidenceEnv is a subprocess environment comparison. It is never accepted.
	EvidenceEnv = "env"
)

// Constraints is the sandbox posture for one task.
type Constraints struct {
	DockerSocket         bool
	ForeignCheckoutWrite bool
	BroadSecretMount     bool
}

// TaskConstraints returns the untrusted posture. Every flag stays closed.
func TaskConstraints(untrusted bool) Constraints {
	_ = untrusted
	return Constraints{}
}

// BoundaryRequest asks for an adapter proof or an env-only check.
type BoundaryRequest struct {
	Evidence string
	Secret   string
	Parent   []string
	Needles  []string
}

// BoundaryResult is the credential-boundary witness.
type BoundaryResult struct {
	Outcome                  string
	EvidenceClass            string
	EnvOnlyIsolationAccepted bool
	LiveBlockedIfUnproven    bool
	Bash                     string
	MCP                      string
}

// ProveCredentialBoundary runs toolbox.bash and the pinned CLI, or records
// that an env-only check is not accepted. A secret visible to sh -c env is
// not isolated. Env-only evidence never returns isolated.
func ProveCredentialBoundary(ctx context.Context, req BoundaryRequest) (BoundaryResult, error) {
	result := BoundaryResult{
		Outcome:                  OutcomeLiveBlocked,
		EvidenceClass:            req.Evidence,
		EnvOnlyIsolationAccepted: false,
		LiveBlockedIfUnproven:    true,
	}
	if req.Evidence != EvidenceAdapter {
		result.EvidenceClass = EvidenceEnv
		return result, nil
	}
	result.EvidenceClass = EvidenceAdapter
	env := scrubEnv(req.Parent, nil)
	if env == nil {
		env = []string{}
	}
	bash := toolbox{env: env}.bash(ctx, "env")
	result.Bash = bash.Output
	bin := lookupEnv(req.Parent, "FACTORY_CLAUDE_BIN")
	spawned := false
	if bin != "" {
		scenario := lookupEnv(req.Parent, "FACTORY_CLAUDE_SCENARIO")
		if scenario == "" {
			scenario = "empty-text"
		}
		cli, cliErr := execClaude(ctx, bin, req.Parent, boundaryNeedles(req), "--print", "--output-format", "stream-json", "--scenario", scenario)
		result.MCP = cli.Stdout + cli.Stderr
		spawned = cliErr == nil
	}
	if !bash.OK || !spawned || secretVisible(result.Bash+"\n"+result.MCP, req) {
		return result, nil
	}
	result.Outcome = OutcomeIsolated
	return result, nil
}

func boundaryNeedles(req BoundaryRequest) []string {
	needles := append([]string{}, req.Needles...)
	if req.Secret != "" {
		needles = append(needles, req.Secret)
	}
	return needles
}

func secretVisible(blob string, req BoundaryRequest) bool {
	if req.Secret != "" && strings.Contains(blob, req.Secret) {
		return true
	}
	for _, needle := range req.Needles {
		if needle != "" && strings.Contains(blob, needle) {
			return true
		}
	}
	return false
}

func isolateEnv(parent, needles []string) []string {
	scrubbed := scrubEnv(parent, []string{"GH_TOKEN", "GITHUB_TOKEN", "PLANTED_SECRET"})
	out := make([]string, 0, len(scrubbed))
	for _, kv := range scrubbed {
		if envContainsNeedle(kv, needles) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func envContainsNeedle(s string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
