// Package coordination is the native activation and budget facade.
// ClaimActivation reads store.GetWake, including InvocationID, and then claims.
// RunUsage is not an admission gate.
package coordination

import (
	"context"

	"github.com/polds/factory/internal/coordination/budget"
	"github.com/polds/factory/internal/store"
)

// OpenTree freezes the roster for one invocation.
func OpenTree(ctx context.Context, st *store.Store, in store.OpenTreeInput) (store.NativePolicy, store.NativeTask, error) {
	return st.OpenNativeTree(ctx, in)
}

// Delegate spawns one child or refuses in the same transaction.
func Delegate(ctx context.Context, st *store.Store, in store.DelegateInput) (store.DelegateResult, error) {
	return st.DelegateNative(ctx, in)
}

// ClaimActivation reads the wake row, then starts one fenced execution.
func ClaimActivation(ctx context.Context, st *store.Store, taskID string, in store.ClaimInput) (store.Activation, error) {
	wake, err := st.GetWake(ctx, taskID)
	if err != nil {
		return store.Activation{}, err
	}
	in.TaskID = wake.TaskID
	in.InvocationID = wake.InvocationID
	return st.ClaimNativeActivation(ctx, in)
}

// Park releases capacity and records blockers.
func Park(ctx context.Context, st *store.Store, in store.ParkInput) (store.Activation, error) {
	return st.ParkNative(ctx, in)
}

// Wake applies one durable wake for a running, parked, completed, or cancelled task.
func Wake(ctx context.Context, st *store.Store, in store.WakeInput) (store.WakeResult, error) {
	wake, err := st.GetWake(ctx, in.TaskID)
	if err != nil {
		return store.WakeResult{}, err
	}
	in.TaskID = wake.TaskID
	in.InvocationID = wake.InvocationID
	return st.ConsumeNativeWake(ctx, in)
}

// WakeCompleted opens a linked run and leaves the prior terminal row immutable.
func WakeCompleted(ctx context.Context, st *store.Store, taskID string, in store.WakeInput) (store.WakeResult, error) {
	wake, err := st.GetWake(ctx, taskID)
	if err != nil {
		return store.WakeResult{}, err
	}
	in.TaskID = wake.TaskID
	in.InvocationID = wake.InvocationID
	return st.WakeCompleted(ctx, in)
}

// RetryExecution opens a new execution of the same run.
func RetryExecution(ctx context.Context, st *store.Store, taskID string) (store.Activation, error) {
	return st.RetryExecution(ctx, taskID)
}

// ResumePark continues a parked task on the same run.
func ResumePark(ctx context.Context, st *store.Store, in store.ClaimInput) (store.Activation, error) {
	return st.ResumePark(ctx, in)
}

// Join applies fail_parent or block_parent and does not synthesize a child.
func Join(ctx context.Context, st *store.Store, in store.JoinInput) (store.JoinResult, error) {
	return st.JoinNative(ctx, in)
}

// CancelSubtree cancels the task and its descendants.
func CancelSubtree(ctx context.Context, st *store.Store, taskID string) (store.SubtreeResult, error) {
	return st.CancelNativeSubtree(ctx, store.SubtreeInput{TaskID: taskID})
}

// PauseSubtree pauses the task and its descendants.
func PauseSubtree(ctx context.Context, st *store.Store, taskID string) (store.SubtreeResult, error) {
	return st.CancelNativeSubtree(ctx, store.SubtreeInput{TaskID: taskID, Pause: true})
}

// BoardResume audits an authorized resume and leaves the cancelled run intact.
func BoardResume(ctx context.Context, st *store.Store, in store.BoardResumeInput) (store.BoardResumeResult, error) {
	return st.BoardResumeNative(ctx, in)
}

// Complete marks the task succeeded or failed.
func Complete(ctx context.Context, st *store.Store, taskID string, failed bool) (store.Activation, error) {
	return st.CompleteNative(ctx, taskID, failed)
}

// Admit refuses new spend when usage is missing or a scope is paused.
func Admit(ctx context.Context, st *store.Store, in store.AdmitInput) (store.AdmitResult, error) {
	return st.Admit(ctx, in)
}

// ReserveAncestors holds cents on every ancestor scope.
func ReserveAncestors(ctx context.Context, st *store.Store, in store.ReserveInput) (store.Reservation, error) {
	return st.ReserveAncestors(ctx, in)
}

// SettleUsage counts one call and attempt.
func SettleUsage(ctx context.Context, st *store.Store, in store.SettleInput) (store.Settlement, error) {
	return st.SettleUsage(ctx, in)
}

// AuthorizeCeiling changes one ceiling and writes the audit row.
func AuthorizeCeiling(ctx context.Context, st *store.Store, in store.CeilingInput) (store.CeilingAudit, error) {
	return st.AuthorizeCeiling(ctx, in)
}

// Rollup adds each leaf once. It does not add the ancestor totals together.
func Rollup(leaves []int64) (invocation, lead, agent, factory int64) {
	return budget.Rollup(leaves)
}

// NoteThreshold counts 80 percent alerts for a charge sequence.
func NoteThreshold(ceiling int64, charges []int64) int {
	return budget.NoteThreshold(ceiling, charges)
}
