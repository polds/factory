package failure

import (
	"embed"
	"encoding/json"
)

//go:embed testdata/oom.json testdata/evicted.json testdata/unschedulable.json testdata/image_pull.json testdata/active_deadline.json testdata/job_create.json testdata/container_exit.json
var fixtures embed.FS

func mustFixture(name string) json.RawMessage {
	b, err := fixtures.ReadFile(name)
	if err != nil {
		panic("failure: " + err.Error())
	}
	return json.RawMessage(b)
}

func codePtr(n int) *int {
	return &n
}

// Cases returns every input/result pair in the classification table.
// T47's parity test drives from this list. Callers must not mutate the
// Details or Evidence slices.
func Cases() []Rule {
	oom := mustFixture("testdata/oom.json")
	evicted := mustFixture("testdata/evicted.json")
	rules := []Rule{
		{
			Name:  "harness_failed_by_agent",
			Input: Input{HarnessStatus: ReasonFailedByAgent},
			Result: Result{
				Class:    ClassAgent,
				Code:     CodeAgentReported,
				Summary:  "Agent reported this run as failed",
				Evidence: []string{"harness status failed_by_agent"},
			},
		},
		{
			Name: "task_completed_harness_failed_by_agent",
			Input: Input{
				RunStatus:     StatusSucceeded,
				HarnessStatus: ReasonFailedByAgent,
			},
			Result: Result{
				Class:    ClassAgent,
				Code:     CodeAgentReported,
				Summary:  "Agent reported this run as failed",
				Evidence: []string{"harness status failed_by_agent"},
			},
		},
		{
			Name: "exit_10_container_exit",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(10),
			},
			Result: Result{
				Class:   ClassAgent,
				Code:    CodeAgentReported,
				Summary: "Agent reported this run as failed (exit 10)",
				Evidence: []string{
					"exit code 10",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "m4_reviewer_exit_10",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(10),
				HarnessStatus: ReasonFailedByAgent,
			},
			Result: Result{
				Class:   ClassAgent,
				Code:    CodeAgentReported,
				Summary: "Agent reported this run as failed (exit 10)",
				Evidence: []string{
					"exit code 10",
					"harness status failed_by_agent",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "exit_10_other_reason",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "agent_invocation",
				ExitCode:      codePtr(10),
			},
			Result: Result{
				Class:   ClassAgent,
				Code:    CodeAgentReported,
				Summary: "Agent reported this run as failed (exit 10)",
				Evidence: []string{
					"exit code 10",
					"failure reason agent_invocation",
				},
			},
		},
		{
			Name: "exit_20",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(20),
			},
			Result: Result{
				Class:    ClassConfiguration,
				Code:     CodeConfigInvalid,
				Summary:  "Configuration is invalid (exit 20)",
				Evidence: []string{"exit code 20"},
			},
		},
		{
			Name: "exit_20_container_exit",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(20),
			},
			Result: Result{
				Class:   ClassConfiguration,
				Code:    CodeConfigInvalid,
				Summary: "Configuration is invalid (exit 20)",
				Evidence: []string{
					"exit code 20",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "reason_config_invalid",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonConfigInvalid,
			},
			Result: Result{
				Class:    ClassConfiguration,
				Code:     CodeConfigInvalid,
				Summary:  "Configuration is invalid",
				Evidence: []string{"failure reason config_invalid"},
			},
		},
		{
			Name: "harness_config_invalid",
			Input: Input{
				HarnessStatus: ReasonConfigInvalid,
			},
			Result: Result{
				Class:    ClassConfiguration,
				Code:     CodeConfigInvalid,
				Summary:  "Configuration is invalid",
				Evidence: []string{"harness status config_invalid"},
			},
		},
		{
			Name: "exit_21",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(21),
			},
			Result: Result{
				Class:     ClassInfrastructure,
				Code:      CodeModelUnavailable,
				Summary:   "Model provider is unavailable (exit 21)",
				Retryable: true,
				Evidence:  []string{"exit code 21"},
			},
		},
		{
			Name: "exit_21_container_exit",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(21),
			},
			Result: Result{
				Class:     ClassInfrastructure,
				Code:      CodeModelUnavailable,
				Summary:   "Model provider is unavailable (exit 21)",
				Retryable: true,
				Evidence: []string{
					"exit code 21",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "reason_model_unavailable",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonModelUnavailable,
			},
			Result: Result{
				Class:     ClassInfrastructure,
				Code:      CodeModelUnavailable,
				Summary:   "Model provider is unavailable",
				Retryable: true,
				Evidence:  []string{"failure reason model_unavailable"},
			},
		},
		{
			Name: "exit_30",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(30),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeEnvironmentSetupFailed,
				Summary:  "Environment setup failed (exit 30)",
				Evidence: []string{"exit code 30"},
			},
		},
		{
			Name: "exit_30_container_exit",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(30),
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeEnvironmentSetupFailed,
				Summary: "Environment setup failed (exit 30)",
				Evidence: []string{
					"exit code 30",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "reason_environment_setup_failed",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonEnvironmentSetupFailed,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeEnvironmentSetupFailed,
				Summary:  "Environment setup failed",
				Evidence: []string{"failure reason environment_setup_failed"},
			},
		},
		{
			Name: "signal_9",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(128 + 9),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeContainerKilled,
				Summary:  "Container was killed by SIGKILL (exit 137)",
				Evidence: []string{"exit code 137 (SIGKILL)"},
			},
		},
		{
			Name: "signal_15",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(128 + 15),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeContainerKilled,
				Summary:  "Container was killed by SIGTERM (exit 143)",
				Evidence: []string{"exit code 143 (SIGTERM)"},
			},
		},
		{
			Name: "snapshot_signal_9",
			Input: Input{
				RunStatus: StatusFailed,
				Details:   json.RawMessage(`{"kubernetes":{"container":{"signal":9}}}`),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeContainerKilled,
				Summary:  "Container was killed by SIGKILL (exit 137)",
				Evidence: []string{"exit code 137 (SIGKILL)"},
			},
		},
		{
			Name: "snapshot_normalized_15",
			Input: Input{
				RunStatus: StatusFailed,
				Details:   json.RawMessage(`{"kubernetes":{"container":{"normalized_exit_code":143}}}`),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeContainerKilled,
				Summary:  "Container was killed by SIGTERM (exit 143)",
				Evidence: []string{"exit code 143 (SIGTERM)"},
			},
		},
		{
			Name: "signal_9_container_exit",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(128 + 9),
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeContainerKilled,
				Summary: "Container was killed by SIGKILL (exit 137)",
				Evidence: []string{
					"exit code 137 (SIGKILL)",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "oom_killed",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerOOM,
				ExitCode:      codePtr(137),
				Details:       oom,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeOOMKilled,
				Summary: "Container was OOMKilled (exit 137)",
				Evidence: []string{
					"exit code 137 (SIGKILL)",
					"failure reason container_oom",
					"termination reason OOMKilled",
					"pod reason Error",
					"pod phase Failed",
					"event reason OOMKilled",
				},
			},
		},
		{
			Name: "oomkilling_event",
			Input: Input{
				RunStatus: StatusFailed,
				Details:   json.RawMessage(`{"kubernetes":{"events":[{"reason":"OOMKilling"}]}}`),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeOOMKilled,
				Summary:  "Container was OOMKilled",
				Evidence: []string{"event reason OOMKilling"},
			},
		},
		{
			Name: "container_oom_without_details",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerOOM,
				ExitCode:      codePtr(137),
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeOOMKilled,
				Summary: "Container was OOMKilled (exit 137)",
				Evidence: []string{
					"exit code 137 (SIGKILL)",
					"failure reason container_oom",
				},
			},
		},
		{
			Name: "evicted",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonEvicted,
				ExitCode:      codePtr(128 + 15),
				Details:       evicted,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeEvicted,
				Summary: "Pod was Evicted (exit 143)",
				Evidence: []string{
					"exit code 143 (SIGTERM)",
					"failure reason evicted",
					"pod reason Evicted",
					"pod phase Failed",
					"event reason Evicted",
					"pod condition reason Evicted",
				},
			},
		},
		{
			Name: "reason_evicted_without_details",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonEvicted,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeEvicted,
				Summary:  "Pod was Evicted",
				Evidence: []string{"failure reason evicted"},
			},
		},
		{
			Name: "container_exit_code_1",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(1),
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeContainerExit,
				Summary: "Container exited (exit 1)",
				Evidence: []string{
					"exit code 1",
					"failure reason container_exit",
				},
			},
		},
		{
			Name: "bare_exit_1",
			Input: Input{
				RunStatus: StatusFailed,
				ExitCode:  codePtr(1),
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeContainerExit,
				Summary:  "Container exited (exit 1)",
				Evidence: []string{"exit code 1"},
			},
		},
		{
			Name: "orphaned",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonOrphaned,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeWorkerLost,
				Summary:  "Worker lost the run (orphaned)",
				Evidence: []string{"failure reason orphaned"},
			},
		},
		{
			Name: "lease_expired",
			Input: Input{
				FailureReason: ReasonLeaseExpired,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeWorkerLost,
				Summary:  "Worker lost the run (lease_expired)",
				Evidence: []string{"failure reason lease_expired"},
			},
		},
		{
			Name: "worker_gone",
			Input: Input{
				FailureReason: ReasonWorkerGone,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeWorkerLost,
				Summary:  "Worker lost the run (worker_gone)",
				Evidence: []string{"failure reason worker_gone"},
			},
		},
		{
			Name:  "run_status_orphaned",
			Input: Input{RunStatus: ReasonOrphaned},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeWorkerLost,
				Summary:  "Worker lost the run (orphaned)",
				Evidence: []string{"run status orphaned"},
			},
		},
		{
			Name: "superseded",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonSuperseded,
			},
			Result: Result{
				Class:    ClassCancelled,
				Code:     CodeSuperseded,
				Summary:  "Run was superseded by a newer building run",
				Evidence: []string{"failure reason superseded_duplicate_building_run"},
			},
		},
		{
			Name:  "cancelled",
			Input: Input{RunStatus: StatusCancelled},
			Result: Result{
				Class:    ClassCancelled,
				Code:     CodeCancelled,
				Summary:  "Run was cancelled",
				Evidence: []string{"run status cancelled"},
			},
		},
		{
			Name: "unknown_reason",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "disk_full",
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeUnknown,
				Summary:  "disk_full",
				Evidence: []string{"failure reason disk_full"},
			},
		},
		{
			Name:  "status_failed_no_signal",
			Input: Input{RunStatus: StatusFailed},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeUnknown,
				Summary:  StatusFailed,
				Evidence: []string{"run status failed"},
			},
		},
		{
			Name:   "empty",
			Input:  Input{},
			Result: Result{},
		},
		{
			Name: "succeeded",
			Input: Input{
				RunStatus: StatusSucceeded,
				ExitCode:  codePtr(0),
			},
			Result: Result{},
		},
	}
	return append(rules, kubernetesCases()...)
}
