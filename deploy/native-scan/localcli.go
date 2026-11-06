package nativescan

import "errors"

// ModeLocalCLI is the local_cli mode. It does not require Linear or a task ref.
const ModeLocalCLI = "local_cli"

// ErrLocalCLI means the request required Linear or a Factory task ref.
var ErrLocalCLI = errors.New("INV-LOCAL-CLI")

// LocalCLIRequest is a local scanner invocation.
// LinearConfigured is true when the caller observed Linear configuration.
// The function does not read the environment.
type LocalCLIRequest struct {
	RequiresLinear   bool
	RequiresTaskRef  bool
	LinearConfigured bool
	TaskRef          string
}

// LocalCLIResult is a zero-finding local run.
type LocalCLIResult struct {
	Mode            string
	Findings        int
	TaskRefRequired bool
	LinearRequired  bool
}

// RunLocalCLI stays available without Linear and without a task ref.
func RunLocalCLI(req LocalCLIRequest) (LocalCLIResult, error) {
	if req.RequiresLinear || req.RequiresTaskRef || req.LinearConfigured {
		return LocalCLIResult{}, ErrLocalCLI
	}
	return LocalCLIResult{
		Mode:            ModeLocalCLI,
		Findings:        0,
		TaskRefRequired: false,
		LinearRequired:  false,
	}, nil
}
