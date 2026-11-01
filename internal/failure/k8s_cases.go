package failure

// kubernetesCases are the upstream Kubernetes backend failure reasons at
// worker 63dbb8a. Exit 10 stays agent. OOM, eviction, container exit, and
// signal exits keep the rows already in Cases. Every other reason is
// infrastructure/unknown so the class matches the SQL backfill CASE.
// A startup preflight failure never becomes a task reason; RBAC denied
// while creating the task Job is job_create.
func kubernetesCases() []Rule {
	unschedulable := mustFixture("testdata/unschedulable.json")
	imagePull := mustFixture("testdata/image_pull.json")
	deadline := mustFixture("testdata/active_deadline.json")
	jobCreate := mustFixture("testdata/job_create.json")
	containerExit := mustFixture("testdata/container_exit.json")

	rules := []Rule{
		{
			Name: "k8s_unschedulable",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "unschedulable",
				Details:       unschedulable,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeUnknown,
				Summary: "unschedulable",
				Evidence: []string{
					"failure reason unschedulable",
					"pod phase Pending",
				},
			},
		},
		{
			Name: "k8s_image_pull",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "image_pull",
				Details:       imagePull,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeUnknown,
				Summary: "image_pull",
				Evidence: []string{
					"failure reason image_pull",
					"pod phase Pending",
					"event reason Failed",
					"container state waiting",
				},
			},
		},
		{
			Name: "k8s_preflight_rbac_job_create",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "job_create",
				Details:       jobCreate,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeUnknown,
				Summary:  "job_create",
				Evidence: []string{"failure reason job_create"},
			},
		},
		{
			Name: "k8s_active_deadline",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "active_deadline",
				Details:       deadline,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeUnknown,
				Summary: "active_deadline",
				Evidence: []string{
					"failure reason active_deadline",
					"pod phase Failed",
				},
			},
		},
		{
			Name: "k8s_active_deadline_sigterm",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: "active_deadline",
				ExitCode:      codePtr(128 + 15),
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeContainerKilled,
				Summary: "Container was killed by SIGTERM (exit 143)",
				Evidence: []string{
					"exit code 143 (SIGTERM)",
					"failure reason active_deadline",
				},
			},
		},
		{
			Name: "k8s_container_exit_nonzero",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(1),
				Details:       containerExit,
			},
			Result: Result{
				Class:   ClassInfrastructure,
				Code:    CodeContainerExit,
				Summary: "Container exited (exit 1)",
				Evidence: []string{
					"exit code 1",
					"failure reason container_exit",
					"termination reason Error",
					"pod reason Error",
					"pod phase Failed",
					"container state terminated",
				},
			},
		},
		{
			Name: "k8s_agent_exit_10",
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: ReasonContainerExit,
				ExitCode:      codePtr(10),
				Details:       containerExit,
			},
			Result: Result{
				Class:   ClassAgent,
				Code:    CodeAgentReported,
				Summary: "Agent reported this run as failed (exit 10)",
				Evidence: []string{
					"exit code 10",
					"failure reason container_exit",
					"termination reason Error",
					"pod reason Error",
					"pod phase Failed",
					"container state terminated",
				},
			},
		},
	}

	// Reasons the Kubernetes backend emits that do not have their own code.
	// An exit code still wins, as k8s_active_deadline_sigterm and
	// k8s_agent_exit_10 show. invalid_image and job_create stay
	// infrastructure, which eligibility already excludes.
	for _, reason := range []string{
		"job_watch",
		"pod_watch",
		"job_failed",
		"volume_mount",
		"init_container",
		"invalid_image",
		"container_create",
		"sidecar_prep",
		"container_wait",
		"task_cancelled",
		"graceful_shutdown",
	} {
		rules = append(rules, Rule{
			Name: "k8s_" + reason,
			Input: Input{
				RunStatus:     StatusFailed,
				FailureReason: reason,
			},
			Result: Result{
				Class:    ClassInfrastructure,
				Code:     CodeUnknown,
				Summary:  reason,
				Evidence: []string{"failure reason " + reason},
			},
		})
	}
	return rules
}
