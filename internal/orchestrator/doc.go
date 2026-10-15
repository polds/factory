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
// caller persists the input request and that status in one transaction,
// releases the worker slot, and ends the execution. The run is not failed
// and is not put back on the queue. Worker loss, lease expiry, and process
// restart leave it parked. A crash after the input request commits and
// before the queue row is deleted is repaired by parking that run again,
// so lease expiry cannot hand it to a worker. ResumeInput returns the work
// item to open and emits Noop. The caller then starts one new execution of
// the same run, on the same conversation, with the question and answer in
// the handoff prompt. That still holds when the run was requeued before
// the answer. The same answer twice does not start a second execution. A
// different answer is a conflict. Cancel from any non-terminal status
// emits MarkCancelled.
package orchestrator
