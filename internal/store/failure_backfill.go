package store

// FailureBackfillCASE is the classification CASE in
// migrations/0006_m5.sql. That file contains this constant verbatim.
//
// Evaluate it against one FROM item with these columns:
//
//	run_status text
//	failure_reason text      // empty string when the run has none
//	exit_code int            // NULL when the run has none
//	harness_status text      // empty string when no terminal event
//	exec_state text          // empty string when the run has no execution
//	details jsonb            // latest failure_details_json, or NULL
//
// The result is class || '/' || code, or NULL when no WHEN matches.
// Codes do not contain '/'. Precedence matches failure.Classify:
// effective exit is exit_code, else the snapshot normalized exit, else
// 128+signal; OOM and Evicted tokens are checked before a signal exit;
// worker_lost is only orphaned, lease_expired, and worker_gone.
//
// harness_status is the payload status of the newest run_events row
// whose type is terminal or run.terminal.
const FailureBackfillCASE = `        CASE
            WHEN
                harness_status = 'failed_by_agent'
                OR failure_effective_exit(exit_code, details) = 10
                THEN 'agent/agent_reported'
            WHEN
                failure_effective_exit(exit_code, details) = 20
                OR failure_reason = 'config_invalid'
                OR harness_status = 'config_invalid'
                THEN 'configuration/config_invalid'
            WHEN
                failure_effective_exit(exit_code, details) = 21
                OR failure_reason = 'model_unavailable'
                OR harness_status = 'model_unavailable'
                THEN 'infrastructure/model_unavailable'
            WHEN
                failure_effective_exit(exit_code, details) = 30
                OR failure_reason = 'environment_setup_failed'
                OR harness_status = 'environment_setup_failed'
                THEN 'infrastructure/environment_setup_failed'
            WHEN
                failure_snapshot_has(details, 'OOMKilled')
                OR failure_snapshot_has(details, 'OOMKilling')
                OR failure_reason = 'container_oom'
                THEN 'infrastructure/oom_killed'
            WHEN
                failure_snapshot_has(details, 'Evicted')
                OR failure_reason = 'evicted'
                THEN 'infrastructure/evicted'
            WHEN failure_effective_exit(exit_code, details) >= 128
                THEN 'infrastructure/container_killed'
            WHEN
                failure_reason = 'container_exit'
                OR (
                    failure_effective_exit(exit_code, details) > 0
                    AND failure_effective_exit(exit_code, details) < 128
                    AND failure_reason = ''
                )
                THEN 'infrastructure/container_exit'
            WHEN
                exec_state IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                OR failure_reason IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                OR run_status IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                THEN 'infrastructure/worker_lost'
            WHEN failure_reason = 'superseded_duplicate_building_run'
                THEN 'cancelled/superseded'
            WHEN run_status = 'cancelled'
                THEN 'cancelled/cancelled'
            WHEN failure_reason <> '' OR run_status = 'failed'
                THEN 'infrastructure/unknown'
        END`
