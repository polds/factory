package protocol

import (
	"encoding/json"
	"time"
)

// TaskState is the serialized terminal task state accepted on the wire.
type TaskState string

const (
	TaskStateCancelled TaskState = "CANCELLED"
)

// TaskAssignment is server → worker.
type TaskAssignment struct {
	TaskID      string `json:"task_id"`
	ExecutionID string `json:"execution_id,omitempty"`
	Task        *Task  `json:"task"`
	DockerImage string `json:"docker_image,omitempty"`
	// SidecarImage is the harness image mounted at /agent.
	SidecarImage        string                     `json:"sidecar_image,omitempty"`
	EnvVars             map[string]string          `json:"env_vars,omitempty"`
	TelemetryCollection *TelemetryCollectionConfig `json:"telemetry_collection,omitempty"`
	AdditionalSidecars  []SidecarMount             `json:"additional_sidecars,omitempty"`
	AdditionalOzArgs    []string                   `json:"additional_oz_args,omitempty"`
	InstanceShape       *InstanceShape             `json:"instance_shape,omitempty"`
}

// TaskCancellation is server → worker.
type TaskCancellation struct {
	TaskID string `json:"task_id"`
}

// TaskClaimed is worker → server.
type TaskClaimed struct {
	TaskID   string `json:"task_id"`
	WorkerID string `json:"worker_id"`
}

// TaskCompleted is worker → server.
type TaskCompleted struct {
	TaskID      string     `json:"task_id"`
	ExecutionID string     `json:"execution_id,omitempty"`
	Message     string     `json:"message"`
	TaskState   *TaskState `json:"task_state,omitempty"`
}

// TaskFailed is worker → server. FailureReason stays orthogonal to agent outcome.
type TaskFailed struct {
	TaskID         string          `json:"task_id"`
	ExecutionID    string          `json:"execution_id,omitempty"`
	Message        string          `json:"message"`
	TaskState      *TaskState      `json:"task_state,omitempty"`
	FailureReason  string          `json:"failure_reason,omitempty"`
	ExitCode       int             `json:"exit_code,omitempty"`
	FailureDetails *FailureDetails `json:"failure_details,omitempty"`
}

// TaskRejected is worker → server. It is backpressure: requeue, do not fail the run.
type TaskRejected struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

// FailureDetails contains backend-specific structured diagnostics.
type FailureDetails struct {
	Kubernetes *KubernetesFailureDetails `json:"kubernetes,omitempty"`
}

// KubernetesFailureDetails is the bounded, privacy-filtered snapshot from docs/04.
type KubernetesFailureDetails struct {
	SchemaVersion     int                                `json:"schema_version"`
	ObservationSource string                             `json:"observation_source,omitempty"`
	ObservedAt        time.Time                          `json:"observed_at"`
	Job               *KubernetesJobFailureDetails       `json:"job,omitempty"`
	Pod               *KubernetesPodFailureDetails       `json:"pod,omitempty"`
	Container         *KubernetesContainerFailureDetails `json:"container,omitempty"`
	Events            []KubernetesEventDetails           `json:"events,omitempty"`
	Truncated         bool                               `json:"truncated,omitempty"`
}

// KubernetesJobFailureDetails identifies a worker-created Job.
type KubernetesJobFailureDetails struct {
	Name       string                       `json:"name,omitempty"`
	UID        string                       `json:"uid,omitempty"`
	Conditions []KubernetesConditionDetails `json:"conditions,omitempty"`
}

// KubernetesPodFailureDetails identifies a task Pod.
type KubernetesPodFailureDetails struct {
	Name              string                       `json:"name,omitempty"`
	UID               string                       `json:"uid,omitempty"`
	Phase             string                       `json:"phase,omitempty"`
	Reason            string                       `json:"reason,omitempty"`
	DeletionTimestamp *time.Time                   `json:"deletion_timestamp,omitempty"`
	Conditions        []KubernetesConditionDetails `json:"conditions,omitempty"`
}

// KubernetesConditionDetails contains the structural condition fields.
type KubernetesConditionDetails struct {
	Type               string     `json:"type,omitempty"`
	Status             string     `json:"status,omitempty"`
	Reason             string     `json:"reason,omitempty"`
	LastTransitionTime *time.Time `json:"last_transition_time,omitempty"`
}

// KubernetesContainerFailureDetails identifies the failing container.
type KubernetesContainerFailureDetails struct {
	Kind               string     `json:"kind,omitempty"`
	Name               string     `json:"name,omitempty"`
	State              string     `json:"state,omitempty"`
	WaitingReason      string     `json:"waiting_reason,omitempty"`
	TerminationReason  string     `json:"termination_reason,omitempty"`
	RawExitCode        *int32     `json:"raw_exit_code,omitempty"`
	NormalizedExitCode *int       `json:"normalized_exit_code,omitempty"`
	Signal             *int32     `json:"signal,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

// KubernetesEventDetails is privacy-filtered Event metadata.
type KubernetesEventDetails struct {
	Type            string     `json:"type,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Count           int32      `json:"count,omitempty"`
	FirstObservedAt *time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt  *time.Time `json:"last_observed_at,omitempty"`
}

// SidecarMount is an extra image mounted into the task container.
type SidecarMount struct {
	Image     string `json:"image"`
	MountPath string `json:"mount_path"`
	ReadWrite bool   `json:"read_write"`
}

// InstanceShape is the resolved compute size. Zero axes are omitted.
type InstanceShape struct {
	Vcpus    int `json:"vcpus,omitempty"`
	MemoryGb int `json:"memory_gb,omitempty"`
}

// TelemetryCollectionConfig is the OTEL bootstrap handed to the worker.
type TelemetryCollectionConfig struct {
	Endpoint       string                   `json:"endpoint"`
	TracingEnabled bool                     `json:"tracing_enabled"`
	LoggingEnabled bool                     `json:"logging_enabled"`
	BootstrapToken *TelemetryBootstrapToken `json:"bootstrap_token,omitempty"`
}

// TelemetryBootstrapToken is a short-lived collector credential.
type TelemetryBootstrapToken struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// Task is the ambient-agent job inside an assignment.
type Task struct {
	ID                  string               `json:"id"`
	Title               string               `json:"title"`
	Definition          TaskDefinition       `json:"task_definition"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	Owner               *TaskOwner           `json:"owner,omitempty"`
	AgentConfigSnapshot *AgentConfigSnapshot `json:"agent_config_snapshot,omitempty"`
	AgentConversationID *string              `json:"agent_conversation_id,omitempty"`
}

// TaskDefinition carries the prompt.
type TaskDefinition struct {
	Prompt string `json:"prompt"`
}

// TaskOwner matches the server's PermissionSubjectAndID JSON (capitalized keys).
type TaskOwner struct {
	Type string `json:"Type"`
	Id   int    `json:"Id"`
}

// AgentConfigSnapshot is the agent configuration frozen onto the run.
type AgentConfigSnapshot struct {
	EnvironmentID             *string                    `json:"environment_id,omitempty"`
	BasePrompt                *string                    `json:"base_prompt,omitempty"`
	ModelID                   *string                    `json:"model_id,omitempty"`
	ProfileID                 *string                    `json:"profile_id,omitempty"`
	SkillSpec                 *string                    `json:"skill_spec,omitempty"`
	MCPServers                map[string]json.RawMessage `json:"mcp_servers,omitempty"`
	ComputerUseEnabled        *bool                      `json:"computer_use_enabled,omitempty"`
	ComputerUseModelID        *string                    `json:"computer_use_model_id,omitempty"`
	IdleTimeoutMinutes        *int                       `json:"idle_timeout_minutes,omitempty"`
	Harness                   *Harness                   `json:"harness,omitempty"`
	HarnessAuthSecrets        *HarnessAuthSecrets        `json:"harness_auth_secrets,omitempty"`
	InferenceProviders        *InferenceProviders        `json:"inference_providers,omitempty"`
	SessionSharing            *SessionSharingConfig      `json:"session_sharing,omitempty"`
	SnapshotDisabled          *bool                      `json:"snapshot_disabled,omitempty"`
	SnapshotUploadTimeoutSecs *int                       `json:"snapshot_upload_timeout_secs,omitempty"`
	SnapshotScriptTimeoutSecs *int                       `json:"snapshot_script_timeout_secs,omitempty"`
	RepositoryHeadOverrides   []RepositoryHeadOverride   `json:"repository_head_overrides,omitempty"`
}

// Harness selects a third-party harness. Nil, empty, or "oz" means the built-in harness.
type Harness struct {
	Type *string `json:"type,omitempty"`
}

// HarnessAuthSecrets names managed secrets injected for a third-party harness.
type HarnessAuthSecrets struct {
	ClaudeAuthSecretName *string `json:"claude_auth_secret_name,omitempty"`
}

// RepositoryHeadType identifies how a prepared repository HEAD is resolved.
type RepositoryHeadType string

const (
	RepositoryHeadTypeCommitSHA RepositoryHeadType = "COMMIT_SHA"
	RepositoryHeadTypeBranch    RepositoryHeadType = "BRANCH"
)

// RepositoryHeadRef is a HEAD type plus value.
type RepositoryHeadRef struct {
	Type  RepositoryHeadType `json:"type"`
	Value string             `json:"value"`
}

// RepositoryIdentity identifies a repository independently of checkout path.
type RepositoryIdentity struct {
	CodeForge string `json:"code_forge"`
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
}

// RepositoryHeadOverride is a server-computed checkout override.
type RepositoryHeadOverride struct {
	CodeForge      string              `json:"code_forge"`
	RepoOwner      string              `json:"repo_owner"`
	RepoName       string              `json:"repo_name"`
	Head           RepositoryHeadRef   `json:"head"`
	CloneFrom      *RepositoryIdentity `json:"clone_from,omitempty"`
	PreserveOrigin bool                `json:"preserve_origin,omitempty"`
}

// AccessLevel is the session-sharing ACL.
type AccessLevel string

const (
	AccessLevelViewer AccessLevel = "VIEWER"
	AccessLevelEditor AccessLevel = "EDITOR"
)

// SessionSharingConfig is the session-sharing choice snapshotted onto the run.
type SessionSharingConfig struct {
	PublicAccess *AccessLevel `json:"public_access,omitempty"`
}

// InferenceProviders carries per-provider inference configuration.
type InferenceProviders struct {
	Aws *AwsInferenceProvider `json:"aws,omitempty"`
}

// AwsInferenceProvider is the Bedrock role block.
type AwsInferenceProvider struct {
	Disabled bool   `json:"disabled,omitempty"`
	RoleARN  string `json:"role_arn,omitempty"`
	Region   string `json:"region,omitempty"`
}
