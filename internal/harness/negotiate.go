package harness

import (
	_ "embed"
	"errors"
	"strings"
)

//go:embed testdata/claude-help.txt
var pinnedClaudeHelp string

const claudeAdapterName = "claude-code"

// Proven is the set of Claude Code capabilities a test has exercised.
// A flag in the CLI help is not enough.
type Proven struct {
	StreamJSON bool
	Resume     bool
	Steer      bool
}

// Capabilities is what Negotiate accepted. NegotiatedSteer stays false
// unless Proven.Steer is set and the help text actually advertises steer.
type Capabilities struct {
	NegotiatedSteer          bool
	Resume                   bool
	StreamJSON               bool
	InternalWorkersInSession bool
	InternalWorkersAreRoster bool
	WorkflowMainLoopOnly     bool
}

// PinnedClaudeHelp is the credential-free help text this build pins.
func PinnedClaudeHelp() string { return pinnedClaudeHelp }

// Negotiate turns CLI help into capabilities. Policy bits for in-session
// workers and main-loop Workflow are fixed. They are not read from the CLI.
func Negotiate(help string, proven Proven) (Capabilities, error) {
	if strings.TrimSpace(help) == "" {
		return Capabilities{}, errors.New("harness: cli help is empty")
	}
	return Capabilities{
		NegotiatedSteer:          proven.Steer && helpHasFlag(help, "--steer"),
		Resume:                   proven.Resume && helpHasFlag(help, "--resume"),
		StreamJSON:               proven.StreamJSON && strings.Contains(help, "--output-format") && strings.Contains(help, "stream-json"),
		InternalWorkersInSession: true,
		InternalWorkersAreRoster: false,
		WorkflowMainLoopOnly:     true,
	}, nil
}

// NegotiatePinned is the production negotiation. Steer is not proven.
func NegotiatePinned(help string) (Capabilities, error) {
	return Negotiate(help, Proven{StreamJSON: true, Resume: true, Steer: false})
}

func helpHasFlag(help, flag string) bool {
	for _, field := range strings.Fields(help) {
		if field == flag {
			return true
		}
	}
	return false
}
