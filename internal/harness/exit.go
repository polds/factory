package harness

// Exit codes from Harness Contract v1 (docs/05-harness-contract.md).
// Only ExitAgentReported is a statement about the agent's work.
const (
	ExitOK               = 0
	ExitAgentReported    = 10
	ExitConfigInvalid    = 20
	ExitModelUnavailable = 21
	ExitEnvironment      = 30
)
