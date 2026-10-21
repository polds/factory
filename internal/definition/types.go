package definition

import "encoding/json"

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
//
// Agents are resolved through Inherit. Routers and skills are applied.
// Automations, scorers, webhooks, and benchmarks are loaded and validated.
type Tree struct {
	Factory        Factory
	Agents         map[string]Agent
	Runners        map[string]Runner
	Routers        map[string]Router
	Skills         map[string]Skill
	AgentSkills    map[string][]Skill
	Automations    map[string]Automation
	Scorers        map[string]Scorer
	Webhooks       map[string]Webhook
	Benchmarks     map[string]BenchmarkSuite
	BenchmarkTasks map[string]map[string]BenchmarkTask
	Digest         string

	// approvalLines is the factory.yaml line of each pipeline.approvals key.
	// approvalsLine is the approvals block. Both stay zero on a tree that was
	// not loaded from a file; WeakensApprovals then reports line 1.
	approvalLines map[string]int
	approvalsLine int
}

// Factory is factory.yaml. Pipeline is applied when present.
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
	Pipeline           *Pipeline               `json:"pipeline,omitempty"`
}

// Pipeline is the declarative stage machine in factory.yaml.
type Pipeline struct {
	Stages        []string                `json:"stages,omitempty"`
	Agents        map[string]string       `json:"agents,omitempty"`
	Skip          map[string]string       `json:"skip,omitempty"`
	Approvals     map[string]ApprovalGate `json:"approvals,omitempty"`
	RevisionLimit int                     `json:"revisionLimit,omitempty"`
}

// ApprovalGate is one pipeline.approvals entry. Approvers is required when
// Required is true.
type ApprovalGate struct {
	Required  bool   `json:"required"`
	Approvers string `json:"approvers,omitempty"`
}

// Router is one routers/<name>.yaml. Name is the file name without .yaml.
// Targets must be concrete models.
type Router struct {
	Name        string
	DisplayName string
	Type        string
	Default     string
	Routes      []RouterRoute
	Complexity  map[string]string
}

// RouterRoute is one prompt-router choice.
type RouterRoute struct {
	Description string
	Model       string
}

// Skill is a SKILL.md. Name is the directory slug.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// Automation is one automations/<name>/automation.md, with execution
// settings resolved onto its target agent. RequiredRepos extends the agent.
type Automation struct {
	Name               string
	Enabled            bool
	Agent              string
	DisplayName        string
	Triggers           []Trigger
	Model              string
	Harness            *Harness
	Runner             string
	Pool               string
	ComputerUseModel   string
	Secrets            []string
	MCPServers         map[string]MCPServer
	RequiredRepos      []Repository
	CredentialStrategy string
	IdleTimeoutMinutes int
	MaxTurns           int
	WorkerHost         string
	Body               string
}

// Trigger is one automation trigger.
type Trigger struct {
	Provider string         `json:"provider,omitempty"`
	Event    string         `json:"event,omitempty"`
	Filter   map[string]any `json:"filter,omitempty"`
	Schedule *Schedule      `json:"schedule,omitempty"`
}

// Schedule is a cron trigger.
type Schedule struct {
	Name string `json:"name,omitempty"`
	Cron string `json:"cron,omitempty"`
}

// Scorer is one scorers/<name>/scorer.md.
type Scorer struct {
	Name            string
	Description     string
	Agents          []ScorerAgent
	Output          string
	Labels          []ScorerLabel
	PassingScore    float64
	SamplingRate    int
	Model           string
	SelfImprovement bool
	Body            string
	// selfImprovementLine is the scorer.md line of the selfImprovement key.
	selfImprovementLine int
}

// ScorerAgent is an agent a scorer samples. A YAML string is the name.
type ScorerAgent struct {
	Name               string `json:"name,omitempty"`
	IncludeDescendants bool   `json:"includeDescendants,omitempty"`
}

// UnmarshalJSON accepts either an agent name or {name, includeDescendants}.
func (a *ScorerAgent) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		a.Name = name
		a.IncludeDescendants = false
		return nil
	}
	var obj struct {
		Name               string `json:"name"`
		IncludeDescendants bool   `json:"includeDescendants"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*a = ScorerAgent{Name: obj.Name, IncludeDescendants: obj.IncludeDescendants}
	return nil
}

// ScorerLabel is one classification label.
type ScorerLabel struct {
	Value       string  `json:"value,omitempty"`
	Description string  `json:"description,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

// Webhook is one webhooks/<name>.yaml. SecretName is a name, not material.
type Webhook struct {
	Name             string
	AuthMode         string
	SignatureScheme  string
	SecretName       string
	DeliveryIDHeader string
	Enabled          bool
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
//
// AllowedUserIDs is the Slack user ids permitted to continue a thread (D12).
// An absent or empty list denies every Slack user. T30 copies this field to
// automation.Allow.SlackUserIDs. A missing list is not "allow every non-bot".
type SlackIntegration struct {
	AutoRespondToThreadReplies bool     `json:"autoRespondToThreadReplies,omitempty"`
	AllowedUserIDs             []string `json:"allowedUserIds,omitempty"`
	Channel                    string   `json:"channel,omitempty"`
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
	Name             string
	Description      string
	AgentType        string
	Model            string
	Harness          *Harness
	Runner           string
	Pool             string
	ComputerUseModel string
	Secrets          []string
	MCPServers       map[string]MCPServer
	// SpawnableBy is who may spawn this agent. After load, an omitted key is
	// [foreman] and an explicit empty list is empty. Nil is only the
	// pre-inherit signal that the key was omitted.
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

// Reviewer pools named by selfImprovement.reviewerType. Admins is the default
// when the key is omitted. improve.Route is the only consumer.
const (
	ReviewerAdmins = "admins"
	ReviewerTeam   = "team"
	ReviewerCustom = "custom"
	ReviewerNone   = "none"
)

// SelfImprovement is the factory self-improvement block. Omitted numeric
// fields stay zero; the improvement engine applies the docs/08 defaults
// (threshold 25, stale 7 days). ReviewerType defaults to admins.
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
