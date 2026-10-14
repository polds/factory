package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Golden files cover the six JSON messages plus cancellation (cancellation is
// the sixth server message; the heartbeat is a ping frame, not JSON).
//
// task_assignment_doc.json is the docs/04 §1 example with comments removed.
// task_assignment.json is the byte-stable encoding of that example.
// task_assignment_m1.json was captured from cmd/m0-spike buildAssignment in
// the M1 offline shape (prefix run_m1_, factory-m1-task / factory-harness).

func TestGoldenCoversSixMessagesAndCancellation(t *testing.T) {
	files := []string{
		"task_assignment.json",
		"task_assignment_doc.json",
		"task_assignment_m1.json",
		"task_cancellation.json",
		"task_claimed.json",
		"task_completed.json",
		"task_failed.json",
		"task_rejected.json",
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join("testdata", name)); err != nil {
			t.Errorf("missing golden %s: %v", name, err)
		}
	}
}

func TestGoldenServerToWorker(t *testing.T) {
	t.Run("task_assignment", func(t *testing.T) {
		assertLiteralMatchesDoc(t, docsAssignment())
		assertRoundTripFile(t, docsAssignment(), "task_assignment.json")
		assertEnvelope(t, TypeTaskAssignment, docsAssignment())
	})
	t.Run("task_cancellation", func(t *testing.T) {
		msg := TaskCancellation{TaskID: "run_01J…"}
		assertRoundTripFile(t, msg, "task_cancellation.json")
		assertEnvelope(t, TypeTaskCancellation, msg)
	})
}

func TestGoldenWorkerToServer(t *testing.T) {
	t.Run("task_claimed", func(t *testing.T) {
		msg := TaskClaimed{TaskID: "run_01J…", WorkerID: "worker-1"}
		assertRoundTripFile(t, msg, "task_claimed.json")
		assertUnmarshalFile(t, msg, "task_claimed.json")
	})
	t.Run("task_completed", func(t *testing.T) {
		msg := taskCompletedExample()
		assertRoundTripFile(t, msg, "task_completed.json")
		assertUnmarshalFile(t, msg, "task_completed.json")
	})
	t.Run("task_failed", func(t *testing.T) {
		msg := taskFailedExample()
		assertRoundTripFile(t, msg, "task_failed.json")
		assertUnmarshalFile(t, msg, "task_failed.json")
	})
	t.Run("task_rejected", func(t *testing.T) {
		msg := TaskRejected{TaskID: "run_01J…", Reason: "at maximum concurrency"}
		assertRoundTripFile(t, msg, "task_rejected.json")
		assertUnmarshalFile(t, msg, "task_rejected.json")
	})
}

func TestM1AssignmentCapturedFromSpike(t *testing.T) {
	raw := readGolden(t, "task_assignment_m1.json")
	var msg TaskAssignment
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.TaskID != "run_m1_20261013T032059" || msg.ExecutionID != "exec_20261013T032059" {
		t.Fatalf("ids %s %s", msg.TaskID, msg.ExecutionID)
	}
	if msg.Task == nil || msg.Task.Title != "M1 offline agent run" {
		t.Fatalf("task %+v", msg.Task)
	}
	if msg.Task.Definition.Prompt == "" || msg.Task.AgentConfigSnapshot == nil ||
		msg.Task.AgentConfigSnapshot.ModelID == nil || *msg.Task.AgentConfigSnapshot.ModelID != "m1-offline-stub" {
		t.Fatalf("snapshot %+v", msg.Task.AgentConfigSnapshot)
	}
	if msg.DockerImage != "factory-m1-task:dev" || msg.SidecarImage != "factory-harness:dev" {
		t.Fatalf("images %s %s", msg.DockerImage, msg.SidecarImage)
	}
	if msg.InstanceShape == nil || msg.InstanceShape.Vcpus != 2 || msg.InstanceShape.MemoryGb != 4 {
		t.Fatalf("shape %+v", msg.InstanceShape)
	}
	if msg.EnvVars["FACTORY_M0_MARKER"] != msg.TaskID || msg.EnvVars["FACTORY_MAX_TURNS"] != "8" ||
		msg.EnvVars["OPENAI_BASE_URL"] != "http://172.17.0.1:9090" || msg.EnvVars["FACTORY_TASK_PROMPT"] == "" {
		t.Fatalf("env keys %+v", msg.EnvVars)
	}
	got, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("m1 assignment is not byte-stable\n got %s\nwant %s", got, raw)
	}
	assertStable(t, msg)
	assertEnvelope(t, TypeTaskAssignment, msg)
}

func assertLiteralMatchesDoc(t *testing.T, literal TaskAssignment) {
	t.Helper()
	var fromDoc TaskAssignment
	if err := json.Unmarshal(readGolden(t, "task_assignment_doc.json"), &fromDoc); err != nil {
		t.Fatal(err)
	}
	if fromDoc.TaskID != "run_01J…" || fromDoc.ExecutionID != "exec_01J…" {
		t.Fatalf("ids %s %s", fromDoc.TaskID, fromDoc.ExecutionID)
	}
	if fromDoc.Task == nil || fromDoc.Task.Title != "Fix broken doc link" || fromDoc.Task.Owner == nil ||
		fromDoc.Task.Owner.Type != "TEAM" || fromDoc.Task.Owner.Id != 42 {
		t.Fatalf("task %+v", fromDoc.Task)
	}
	if fromDoc.Task.AgentConversationID == nil || *fromDoc.Task.AgentConversationID != "conv_01J…" {
		t.Fatal("conversation")
	}
	snap := fromDoc.Task.AgentConfigSnapshot
	if snap == nil || snap.Harness == nil || snap.Harness.Type == nil || *snap.Harness.Type != "claude" {
		t.Fatal("harness")
	}
	if snap.SnapshotDisabled == nil || *snap.SnapshotDisabled {
		t.Fatal("snapshot_disabled")
	}
	if snap.InferenceProviders == nil || snap.InferenceProviders.Aws == nil ||
		snap.InferenceProviders.Aws.Disabled || snap.InferenceProviders.Aws.Region != "us-west-2" {
		t.Fatal("inference")
	}
	if fromDoc.DockerImage != "ghcr.io/acme/payments-ci:2026-10" ||
		fromDoc.SidecarImage != "ghcr.io/acme/factory-harness-claude:1.4.0" {
		t.Fatal("images")
	}
	if len(fromDoc.AdditionalSidecars) != 1 || fromDoc.AdditionalSidecars[0].ReadWrite {
		t.Fatalf("sidecars %+v", fromDoc.AdditionalSidecars)
	}
	if fromDoc.EnvVars["GITHUB_ACCESS_TOKEN"] != "…" || fromDoc.EnvVars["FACTORY_API_KEY"] != "…" {
		t.Fatal(fromDoc.EnvVars)
	}
	if fromDoc.InstanceShape == nil || fromDoc.InstanceShape.Vcpus != 4 || fromDoc.InstanceShape.MemoryGb != 8 {
		t.Fatal(fromDoc.InstanceShape)
	}
	if fromDoc.TelemetryCollection == nil || !fromDoc.TelemetryCollection.TracingEnabled ||
		fromDoc.TelemetryCollection.BootstrapToken == nil ||
		fromDoc.TelemetryCollection.BootstrapToken.ExpiresAt != "2026-10-12T20:00:00Z" {
		t.Fatal(fromDoc.TelemetryCollection)
	}

	got, err := json.Marshal(fromDoc)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(literal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("docs/04 example does not match literal\n doc %s\nlit %s", got, want)
	}
	assertStable(t, fromDoc)
}

func assertRoundTripFile[T any](t *testing.T, v T, name string) {
	t.Helper()
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, name)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s\n got %s\nwant %s", name, got, want)
	}
	assertStable(t, v)
}

func assertUnmarshalFile[T any](t *testing.T, want T, name string) {
	t.Helper()
	var got T
	if err := json.Unmarshal(readGolden(t, name), &got); err != nil {
		t.Fatal(err)
	}
	gb, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gb, wb) {
		t.Fatalf("unmarshal %s\n got %s\nwant %s", name, gb, wb)
	}
}

func assertEnvelope(t *testing.T, typ string, payload any) {
	t.Helper()
	raw, err := Marshal(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != typ {
		t.Fatalf("type %s", env.Type)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(env.Data, body) {
		t.Fatalf("envelope data\n got %s\nwant %s", env.Data, body)
	}
	again, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again) {
		t.Fatalf("envelope unstable\n first %s\nsecond %s", raw, again)
	}
}

func assertStable[T any](t *testing.T, v T) {
	t.Helper()
	first, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded T
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("not byte-stable\n first %s\nsecond %s", first, second)
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(b, "\n")
}

func docsAssignment() TaskAssignment {
	created := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)
	public := AccessLevelViewer
	return TaskAssignment{
		TaskID:      "run_01J…",
		ExecutionID: "exec_01J…",
		Task: &Task{
			ID:         "run_01J…",
			Title:      "Fix broken doc link",
			Definition: TaskDefinition{Prompt: "…"},
			CreatedAt:  created,
			UpdatedAt:  created,
			Owner:      &TaskOwner{Type: "TEAM", Id: 42},
			AgentConfigSnapshot: &AgentConfigSnapshot{
				EnvironmentID:      ptr("env_01J…"),
				BasePrompt:         ptr("…"),
				ModelID:            ptr("claude-5-sonnet-high"),
				ProfileID:          ptr("prof_01J…"),
				SkillSpec:          ptr("…"),
				MCPServers:         map[string]json.RawMessage{"sentry": json.RawMessage(`{}`)},
				ComputerUseEnabled: ptr(true),
				ComputerUseModelID: ptr("claude-5-sonnet-high"),
				IdleTimeoutMinutes: ptr(15),
				Harness:            &Harness{Type: ptr("claude")},
				HarnessAuthSecrets: &HarnessAuthSecrets{ClaudeAuthSecretName: ptr("ANTHROPIC_API_KEY")},
				InferenceProviders: &InferenceProviders{Aws: &AwsInferenceProvider{
					Disabled: false,
					RoleARN:  "arn:…",
					Region:   "us-west-2",
				}},
				SessionSharing:            &SessionSharingConfig{PublicAccess: &public},
				SnapshotDisabled:          ptr(false),
				SnapshotUploadTimeoutSecs: ptr(300),
				SnapshotScriptTimeoutSecs: ptr(120),
				RepositoryHeadOverrides: []RepositoryHeadOverride{{
					CodeForge:      "GITHUB",
					RepoOwner:      "acme",
					RepoName:       "payments-docs",
					Head:           RepositoryHeadRef{Type: RepositoryHeadTypeCommitSHA, Value: "0123…4567"},
					CloneFrom:      &RepositoryIdentity{CodeForge: "GITHUB", Owner: "acme-bench", Repo: "payments-docs"},
					PreserveOrigin: true,
				}},
			},
			AgentConversationID: ptr("conv_01J…"),
		},
		DockerImage:  "ghcr.io/acme/payments-ci:2026-10",
		SidecarImage: "ghcr.io/acme/factory-harness-claude:1.4.0",
		EnvVars: map[string]string{
			"GITHUB_ACCESS_TOKEN": "…",
			"FACTORY_API_KEY":     "…",
		},
		TelemetryCollection: &TelemetryCollectionConfig{
			Endpoint:       "https://otel.internal:4318",
			TracingEnabled: true,
			LoggingEnabled: true,
			BootstrapToken: &TelemetryBootstrapToken{Token: "…", ExpiresAt: "2026-10-12T20:00:00Z"},
		},
		AdditionalSidecars: []SidecarMount{{
			Image:     "ghcr.io/acme/toolbox:1",
			MountPath: "/mnt/toolbox",
			ReadWrite: false,
		}},
		AdditionalOzArgs: []string{"--skill", "repository-conventions"},
		InstanceShape:    &InstanceShape{Vcpus: 4, MemoryGb: 8},
	}
}

func taskCompletedExample() TaskCompleted {
	state := TaskStateCancelled
	return TaskCompleted{
		TaskID:      "run_01J…",
		ExecutionID: "exec_01J…",
		Message:     "agent exited 0",
		TaskState:   &state,
	}
}

func taskFailedExample() TaskFailed {
	state := TaskStateCancelled
	observed := time.Date(2026, 10, 12, 19, 5, 0, 0, time.UTC)
	started := time.Date(2026, 10, 12, 19, 1, 0, 0, time.UTC)
	finished := time.Date(2026, 10, 12, 19, 4, 30, 0, time.UTC)
	rawExit := int32(9)
	norm := 137
	sig := int32(9)
	return TaskFailed{
		TaskID:        "run_01J…",
		ExecutionID:   "exec_01J…",
		Message:       "container was killed",
		TaskState:     &state,
		FailureReason: "environment_setup_failed",
		ExitCode:      137,
		FailureDetails: &FailureDetails{
			Kubernetes: &KubernetesFailureDetails{
				SchemaVersion:     1,
				ObservationSource: "worker",
				ObservedAt:        observed,
				Job: &KubernetesJobFailureDetails{
					Name: "run-01j",
					UID:  "job-uid",
					Conditions: []KubernetesConditionDetails{{
						Type:   "Failed",
						Status: "True",
						Reason: "BackoffLimitExceeded",
					}},
				},
				Pod: &KubernetesPodFailureDetails{
					Name:   "run-01j-pod",
					UID:    "pod-uid",
					Phase:  "Failed",
					Reason: "Error",
				},
				Container: &KubernetesContainerFailureDetails{
					Kind:               "task",
					Name:               "task",
					State:              "terminated",
					TerminationReason:  "Error",
					RawExitCode:        &rawExit,
					NormalizedExitCode: &norm,
					Signal:             &sig,
					StartedAt:          &started,
					FinishedAt:         &finished,
				},
				Events: []KubernetesEventDetails{{
					Type:            "Warning",
					Reason:          "BackOff",
					Count:           3,
					FirstObservedAt: &started,
					LastObservedAt:  &finished,
				}},
				Truncated: true,
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }
