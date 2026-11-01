// Package failure is the only classifier of a failed run.
//
// Classify answers one question: was this the agent's fault, or the
// infrastructure's? The API returns the Result and the console renders it.
// Nothing else re-derives the class from a failure reason or an exit code.
//
// # M4 reviewer run exit 10
//
// Reviewer run run_01M53XJ9A6NZ62RF26AK9ZDNQK exited 10 and was stored as
// failure_reason container_exit. That is the worker's label for the exit, not
// a harness bug.
//
// docs/05 gives exit 10 a single meaning: the agent reported that its own work
// failed. cmd/factory-harness implements that as ExitAgentReported (10) and
// writes one terminal event whose status is failed_by_agent. The same code
// covers a turn budget that runs out. The process then exits 10. It does not
// exit 0, and it does not ask the worker to send task_completed.
//
// docs/05's control-plane column says exit 10 should become task_completed
// with the run marked failed_by_agent. The upstream worker does not implement
// that column. Factory runs the unmodified worker pinned at 63dbb8ae8ccb
// (deploy/worker). On the Docker backend, which is what the M4 laptop run
// used, any non-zero container status becomes task_failed. The reason is
// container_exit, or container_oom when inspect reports OOMKilled. The status
// code is copied into exit_code unchanged. Zero is the only status that
// becomes task_completed. There is no branch for 10, 20, 21, or 30.
//
// factoryd stores that task_failed payload as it arrived. Server.Terminal
// writes failure_reason and exit_code through to the execution and the run.
// It does not read the harness terminal event and it does not remap exit 10.
// The run is therefore status failed, failure_reason container_exit, exit_code
// 10, while the harness event (when event push is on) still says
// failed_by_agent.
//
// Exit 10 is the contract's agent-reported failure. Classify treats exit 10
// with any worker reason, including container_exit, and a harness terminal
// status of failed_by_agent, as agent / agent_reported. A reviewer retry must
// not treat that shape as infrastructure.
//
// # Precedence
//
// The first matching row wins:
//
//  1. Harness status failed_by_agent, or exit 10.
//  2. Exit 20, or reason or harness status config_invalid.
//  3. Exit 21, or reason or harness status model_unavailable (retryable).
//  4. Exit 30, or reason or harness status environment_setup_failed.
//  5. OOMKilled or OOMKilling in the kubernetes snapshot, or reason container_oom.
//  6. Evicted in the snapshot, or reason evicted.
//  7. Exit >= 128 (128+signal), including a snapshot signal when the input
//     exit code is absent.
//  8. Reason container_exit, or any other exit in 1..127 with an empty reason.
//  9. Reason or run status orphaned, lease_expired, or worker_gone.
//  10. Reason superseded_duplicate_building_run.
//  11. Run status cancelled.
//  12. Any other non-empty failure reason.
//  13. Run status failed with no reason, exit, harness status, or snapshot.
//  14. Otherwise the run is not failed and the class is empty.
//
// Cases also covers shapes the contract table does not name, so the T47
// parity check sees them: reason container_oom with no kubernetes snapshot
// (the Docker backend's OOM path), a kubelet event reason OOMKilling, reason
// evicted with no snapshot, an exit in 1..127 with an empty reason, a
// snapshot signal or normalized exit when Input.ExitCode is nil, and run
// status failed with nothing else.
//
// A cancelled run that also carries exit 10 or a signal matches the earlier
// row. Server.finish keeps the worker exit code when it forces status
// cancelled, so both can be stored together. The task_completed cancel path
// has no exit code and matches row 11.
//
// Summary and Evidence are built from exit numbers, signal names, known
// reason codes, and the whitelisted kubernetes fields (pod phase and reason,
// container state, waiting and termination reasons, condition and event
// reasons). Message text and any other free-text snapshot field are ignored.
// An unrecognized failure reason is the one exception: the unknown summary
// names that reason. Evidence is at most six lines.
//
// Orphaned, lease_expired, and worker_gone are the worker_lost codes. ExpireLeases
// today marks the execution orphaned and requeues the run without a failure
// reason, so classifying that run row yields an empty class. Pass the
// execution state as RunStatus or FailureReason orphaned to classify the attempt.
//
// # Kubernetes reasons
//
// The upstream worker's Kubernetes backend reports a failure_reason from its
// metric list (unschedulable, image_pull, job_create, active_deadline, and
// the other kubernetes.go reasons) and a failure_details.kubernetes snapshot.
// Exit 10 stays agent even when that snapshot is present. OOMKilled and
// Evicted keep their own rows. Any other non-zero container exit keeps
// container_exit, unless the exit is 20, 21, 30, or 128+signal. Every
// remaining Kubernetes reason is infrastructure with code unknown, and the
// summary is that reason. Configuration stays exit 20 or config_invalid.
// infrastructure and configuration are the classes eligibility already
// excludes.
//
// A startup preflight or RBAC denial does not arrive as its own reason. It
// either stops the worker before a task exists, or task Job creation fails
// and the reason is job_create.
//
// Cases is the whole table. The T47 parity check compares its SQL CASE to
// these inputs. Kubernetes rows use the same codes as that CASE.
package failure
