package definition

// Severity values for Diagnostic.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Diagnostic is one validation finding. File is relative to the definition
// root and uses forward slashes. Line is 1-based.
type Diagnostic struct {
	File     string
	Line     int
	Severity string
	Message  string
}

// Tree is a parsed definition. Digest is a SHA-256 hex digest of every file
// in the directory. It is stable across runs and changes when any file's
// path or contents change.
type Tree struct {
	Factory Factory
	Agents  map[string]Agent
	Runners map[string]Runner
	Digest  string
}

// Factory is the M2 view of factory.yaml. Pipeline and other M3-only
// sections are not stored.
type Factory struct {
	SchemaVersion      string                  `json:"schemaVersion,omitempty"`
	Name               string                  `json:"name,omitempty"`
	Description        string                  `json:"description,omitempty"`
	Alias              string                  `json:"alias,omitempty"`
	CodeForges         []string                `json:"codeForges,omitempty"`
	Repositories       []Repository            `json:"repositories,omitempty"`
	CredentialStrategy string                  `json:"credentialStrategy,omitempty"`
	Integrations       []Integration           `json:"integrations,omitempty"`
	Secrets            []string                `json:"secrets,omitempty"`
	MCPServers         map[string]MCPServer    `json:"mcpServers,omitempty"`
	AgentDefaults      AgentDefaults           `json:"agentDefaults,omitempty"`
	Inference          *Inference              `json:"inference,omitempty"`
	SelfImprovement    *SelfImprovement        `json:"selfImprovement,omitempty"`
	ScorerDefaults     *ScorerDefaults         `json:"scorerDefaults,omitempty"`
	BenchmarkSubs      []BenchmarkSubstitution `json:"benchmarkRepoSubstitutions,omitempty"`
	CloudProviders     *CloudProviders         `json:"cloudProviders,omitempty"`
}

// Repository is a code forge repository reference.
type Repository struct {
	CodeForge string `json:"codeForge,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Name      string `json:"name,omitempty"`
	Deferred  bool   `json:"deferred,omitempty"`
}

// Integration is one factory integration entry.
type Integration struct {
	Type   string            `json:"type,omitempty"`
	Slack  *SlackIntegration `json:"slack,omitempty"`
	Linear map[string]any    `json:"linear,omitempty"`
	Jira   map[string]any    `json:"jira,omitempty"`
}

// SlackIntegration holds Slack integration settings.
type SlackIntegration struct {
	AutoRespondToThreadReplies bool `json:"autoRespondToThreadReplies,omitempty"`
}

// AgentDefaults are factory-wide defaults. An agent's model and harness
// replace these; they are never combined.
type AgentDefaults struct {
	Model              string               `json:"model,omitempty"`
	Harness            *Harness             `json:"harness,omitempty"`
	Runner             string               `json:"runner,omitempty"`
	Pool               string               `json:"pool,omitempty"`
	ComputerUseModel   string               `json:"computerUseModel,omitempty"`
	Secrets            []string             `json:"secrets,omitempty"`
	MCPServers         map[string]MCPServer `json:"mcpServers,omitempty"`
	WorkerHost         string               `json:"workerHost,omitempty"`
	CredentialStrategy string               `json:"credentialStrategy,omitempty"`
	IdleTimeoutMinutes int                  `json:"idleTimeoutMinutes,omitempty"`
	MaxTurns           int                  `json:"maxTurns,omitempty"`
}

// Agent is one agents/<slug>/agent.md. Name is the directory slug.
// Model, Harness, Runner, Secrets, and MCPServers are the effective
// values after applying agentDefaults. An agent list or map replaces the
// default instead of merging; an omitted key inherits. Factory-wide
// secrets and MCP servers stay on Factory and always apply in addition.
//
// MaxTurns is the agent's maxTurns value, or 0 when unset (callers use 40).
type Agent struct {
	Name               string
	Description        string
	AgentType          string
	Model              string
	Harness            *Harness
	Runner             string
	Pool               string
	ComputerUseModel   string
	Secrets            []string
	MCPServers         map[string]MCPServer
	SpawnableBy        []string
	CredentialStrategy string
	IdleTimeoutMinutes int
	RequiredRepos      []Repository
	MaxTurns           int
	WorkerHost         string
	Body               string
}

// Harness is an explicit harness block. It is mutually exclusive with Model.
type Harness struct {
	Type  string       `json:"type,omitempty"`
	Model string       `json:"model,omitempty"`
	Auth  *HarnessAuth `json:"auth,omitempty"`
}

// HarnessAuth is a harness credential reference. SecretName is a name, not material.
type HarnessAuth struct {
	Source     string `json:"source,omitempty"`
	SecretName string `json:"secretName,omitempty"`
}

// MCPServer is one MCP server entry. Header and env values must be
// {{SECRET_NAME}} references, not inline credentials.
type MCPServer struct {
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	WarpID    *string           `json:"warpId,omitempty"`
	ManagedID string            `json:"managedId,omitempty"`
}

// Runner is one runners/<name>.yaml. Name is the file name without .yaml.
// DockerImage is platform.linux.dockerImage.
type Runner struct {
	Name                           string
	Description                    string
	DockerImage                    string
	SetupCommands                  []string
	VCPUs                          int
	MemoryGB                       int
	OS                             string
	Arch                           string
	RegistryCredentialSecretName   string
	FailureSessionRetentionMinutes int
}

// Inference is the factory inference block.
type Inference struct {
	Default   InferenceDefault             `json:"default,omitempty"`
	Providers map[string]InferenceProvider `json:"providers,omitempty"`
}

// InferenceDefault selects the default provider.
type InferenceDefault struct {
	Provider      string `json:"provider,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
}

// InferenceProvider is one named provider.
type InferenceProvider struct {
	Kind          string `json:"kind,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
	Region        string `json:"region,omitempty"`
	RoleArn       string `json:"roleArn,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
}

// SelfImprovement is recorded but not evaluated in M2.
type SelfImprovement struct {
	FailedRunThreshold int      `json:"failedRunThreshold,omitempty"`
	StaleFailureDays   int      `json:"staleFailureDays,omitempty"`
	ReviewerType       string   `json:"reviewerType,omitempty"`
	ReviewerEmails     []string `json:"reviewerEmails,omitempty"`
}

// ScorerDefaults is recorded but not evaluated in M2.
type ScorerDefaults struct {
	Runner     string               `json:"runner,omitempty"`
	Model      string               `json:"model,omitempty"`
	Secrets    []string             `json:"secrets,omitempty"`
	MCPServers map[string]MCPServer `json:"mcpServers,omitempty"`
}

// BenchmarkSubstitution is a benchmark repo rewrite. Not applied in M2.
type BenchmarkSubstitution struct {
	Source Repository `json:"source,omitempty"`
	Target Repository `json:"target,omitempty"`
}

// CloudProviders is optional cloud identity configuration. Not applied in M2.
type CloudProviders struct {
	AWS *CloudAWS `json:"aws,omitempty"`
	GCP *CloudGCP `json:"gcp,omitempty"`
}

// CloudAWS is an AWS role binding.
type CloudAWS struct {
	RoleArn string `json:"roleArn,omitempty"`
}

// CloudGCP is a GCP workload-identity binding.
type CloudGCP struct {
	ProjectNumber                        string `json:"projectNumber,omitempty"`
	WorkloadIdentityFederationPoolID     string `json:"workloadIdentityFederationPoolId,omitempty"`
	WorkloadIdentityFederationProviderID string `json:"workloadIdentityFederationProviderId,omitempty"`
	ServiceAccountEmail                  string `json:"serviceAccountEmail,omitempty"`
}
