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
// Codes do not contain '/'. An empty failure reason on a non-cancelled
// run does not match, so the backfill leaves failure_class NULL.
//
// Exit 10 and harness status failed_by_agent are agent/agent_reported
// before container_exit is considered. superseded_duplicate_building_run
// is cancelled/superseded before a cancelled status becomes
// cancelled/cancelled.
//
// worker_lost matches execution state orphaned and failure_reason
// orphaned, lease_expired, lease_expiry, worker_gone, or worker_lost.
// OOMKilled and Evicted match the kubernetes container termination
// reason, the pod reason, or events[].reason.
//
// harness_status is the payload status of the newest run_events row
// whose type is terminal or run.terminal.
const FailureBackfillCASE = `        CASE
            WHEN harness_status = 'failed_by_agent' OR exit_code = 10
                THEN 'agent/agent_reported'
            WHEN exit_code = 20 OR failure_reason = 'config_invalid'
                THEN 'configuration/config_invalid'
            WHEN exit_code = 21 OR failure_reason = 'model_unavailable'
                THEN 'infrastructure/model_unavailable'
            WHEN
                exit_code = 30
                OR failure_reason = 'environment_setup_failed'
                THEN 'infrastructure/environment_setup_failed'
            WHEN
                details #>> '{kubernetes,container,termination_reason}'
                = 'OOMKilled'
                OR details #>> '{kubernetes,pod,reason}' = 'OOMKilled'
                OR jsonb_path_exists(
                    details,
                    (
                        '$.kubernetes.events[*].reason'
                        || ' ? (@ == "OOMKilled")'
                    )::jsonpath
                )
                THEN 'infrastructure/oom_killed'
            WHEN
                details #>> '{kubernetes,pod,reason}' = 'Evicted'
                OR details #>> '{kubernetes,container,termination_reason}'
                = 'Evicted'
                OR jsonb_path_exists(
                    details,
                    (
                        '$.kubernetes.events[*].reason'
                        || ' ? (@ == "Evicted")'
                    )::jsonpath
                )
                THEN 'infrastructure/evicted'
            WHEN exit_code >= 128
                THEN 'infrastructure/container_killed'
            WHEN failure_reason = 'container_exit'
                THEN 'infrastructure/container_exit'
            WHEN
                exec_state = 'orphaned'
                OR failure_reason IN (
                    'orphaned',
                    'lease_expired',
                    'lease_expiry',
                    'worker_gone',
                    'worker_lost'
                )
                THEN 'infrastructure/worker_lost'
            WHEN failure_reason = 'superseded_duplicate_building_run'
                THEN 'cancelled/superseded'
            WHEN run_status = 'cancelled'
                THEN 'cancelled/cancelled'
            WHEN failure_reason <> ''
                THEN 'infrastructure/unknown'
        END`
