package linear

import "strings"

// task_ref is the Factory native task id on a synthetic brief.
// The scanner baseline remains thread.kind linear_issue in doc.go.
// Native mode leaves linear_issue null and does not require Linear.
const nativeTaskPrefix = "ntask_"

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NativeBrief is a Factory scanner brief. LinearIssue is null.
type NativeBrief struct {
	Mode           string
	TaskRef        string
	LinearIssue    *string
	RequiresLinear bool
}

// ErrNativeBrief means the id is not a Factory task_ref.
var ErrNativeBrief = briefError("INV-TASK-REF")

type briefError string

func (e briefError) Error() string { return string(e) }

// SyntheticNativeBrief builds a native brief from a Factory task_ref.
// A Linear issue id is rejected. RequiresLinear is false.
func SyntheticNativeBrief(taskRef string) (NativeBrief, error) {
	if !validTaskRef(taskRef) {
		return NativeBrief{}, ErrNativeBrief
	}
	return NativeBrief{
		Mode:           "native",
		TaskRef:        taskRef,
		LinearIssue:    nil,
		RequiresLinear: false,
	}, nil
}

func validTaskRef(id string) bool {
	body, ok := strings.CutPrefix(id, nativeTaskPrefix)
	if !ok || len(body) != 26 {
		return false
	}
	for _, r := range body {
		if !strings.ContainsRune(crockford, r) {
			return false
		}
	}
	return true
}
