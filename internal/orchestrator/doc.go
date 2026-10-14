// Package orchestrator is the pure work-item stage machine.
//
// Step applies one event to a pipeline definition and a work-item state and
// returns the next state plus the effects the caller must perform. It does
// not touch the clock, the network, or the database. Infrastructure retries
// stay on the M2 lease path and never become events here.
//
// Stages run in Def.Stages order. Created starts the first stage whose skip
// predicate is not true. RunSucceeded advances to the next such stage.
// whenScopeClear and whenIntakeHasScope are the only predicate names. A nil
// predicate function evaluates false. M3Predicates returns false for both,
// so an M3 caller never skips (docs/12, decision D2). A predicate the caller
// sets to true does skip. Skipping building also skips the spec gate, because
// that gate only guards the building run.
//
// approvals.spec, when required, blocks entry to building: Step emits
// RequestApproval and does not emit StartRun until Approved for that gate.
// approvals.handoff, when required, blocks leaving the last run stage.
// When human_handoff is itself a stage, the gate blocks that run; otherwise
// approval completes the work item. approvals.merge is not a guard. Branch
// protection owns it.
//
// Rejected returns the work item to planning and starts that stage again.
// It does not cancel the work item.
//
// RunFailed on reviewing is a revision. Step increments RevisionCount and
// starts building without asking for the spec gate again. When the
// incremented count is greater than RevisionLimit, Step emits Escalate and
// does not start a run. RunFailed on any other stage leaves the work item
// open, emits Escalate, and does not start a run.
//
// AskInput moves an open work item to awaiting_input and emits Noop. The
// caller releases the worker slot and keeps the logical run suspended
// (docs/02, docs/05). ResumeInput returns to open and does not start a
// run. Cancel from any non-terminal status emits MarkCancelled.
package orchestrator
