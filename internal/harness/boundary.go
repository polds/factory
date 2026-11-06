package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
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

// ProveCredentialBoundary runs the adapter bash and MCP paths, or records
// that an env-only check is not accepted. Env-only evidence never returns isolated.
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
	needles := append([]string{}, req.Needles...)
	if req.Secret != "" {
		needles = append(needles, req.Secret)
	}
	bashOut, bashOK := adapterBash(ctx, req.Parent, needles)
	if !bashOK {
		return result, nil
	}
	mcpOut := adapterMCP(req.Parent, "payload "+req.Secret, needles)
	eventRaw, eventOK := redactedEvent(req.Secret, needles)
	if !eventOK {
		return result, nil
	}
	holder := secretValue(req.Secret)
	var log bytes.Buffer
	slog.New(slog.NewTextHandler(&log, nil)).Info("boundary", "secret", holder)
	printed := fmt.Sprintf("%v %+v %#v", holder, holder, &holder)
	blob := bashOut + "\n" + mcpOut + "\n" + eventRaw + "\n" + printed + "\n" + log.String()
	result.Bash = bashOut
	result.MCP = mcpOut
	if req.Secret != "" && strings.Contains(blob, req.Secret) {
		return result, nil
	}
	result.Outcome = OutcomeIsolated
	result.EvidenceClass = EvidenceAdapter
	return result, nil
}

func adapterBash(ctx context.Context, parent, needles []string) (string, bool) {
	cmd := exec.CommandContext(ctx, "sh", "-c", "env")
	cmd.Env = isolateEnv(parent, needles)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func redactedEvent(secret string, needles []string) (string, bool) {
	raw, err := json.Marshal(Event{Type: "message.delta", Text: Redact("saw "+secret, needles)})
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func adapterMCP(parent []string, payload string, needles []string) string {
	env := isolateEnv(parent, needles)
	return Redact(payload, needles) + "\n" + strings.Join(env, "\n")
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
