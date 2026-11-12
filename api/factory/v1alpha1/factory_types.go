package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Factory is the factory.yaml document for one factory.
// metadata.name is the factoryd factory name (D12).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type Factory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FactorySpec   `json:"spec,omitempty"`
	Status            FactoryStatus `json:"status,omitempty"`
}

// FactorySpec is the desired factory definition.
//
// +kubebuilder:validation:XValidation:rule="!has(self.publish) || !has(self.publish.enabled) || self.publish.enabled == false || (has(self.budget) && has(self.budget.ceilingCents) && self.budget.ceilingCents > 0)",message="budget.ceilingCents is required when publish.enabled is true"
// +kubebuilder:validation:XValidation:rule="!has(self.roleSource) || (has(self.budget) && has(self.budget.ceilingCents) && self.budget.ceilingCents > 0)",message="budget.ceilingCents is required when roleSource is set"
type FactorySpec struct {
	SchemaVersion      string                   `json:"schemaVersion,omitempty"`
	DisplayName        string                   `json:"displayName,omitempty"`
	Description        string                   `json:"description,omitempty"`
	Alias              string                   `json:"alias,omitempty"`
	CodeForges         []string                 `json:"codeForges,omitempty"`
	Repositories       []FactoryRepositorySpec  `json:"repositories,omitempty"`
	RoleSource         *RoleSourceSpec          `json:"roleSource,omitempty"`
	Native             *NativeSpec              `json:"native,omitempty"`
	Publish            *PublishSpec             `json:"publish,omitempty"`
	Evidence           *EvidenceSpec            `json:"evidence,omitempty"`
	Budget             *BudgetSpec              `json:"budget,omitempty"`
	CredentialStrategy string                   `json:"credentialStrategy,omitempty"`
	Secrets            []string                 `json:"secrets,omitempty"`
	SecretRefs         []SecretRef              `json:"secretRefs,omitempty"`
	MCPServers         map[string]MCPServerSpec `json:"mcpServers,omitempty"`
	AgentDefaults      *AgentDefaultsSpec       `json:"agentDefaults,omitempty"`
	Inference          *InferenceSpec           `json:"inference,omitempty"`
	Pipeline           *PipelineSpec            `json:"pipeline,omitempty"`
	SelfImprovement    *SelfImprovementSpec     `json:"selfImprovement,omitempty"`
	ScorerDefaults     *ScorerDefaultsSpec      `json:"scorerDefaults,omitempty"`
	// Files is the lossless factory.yaml. When set, convert prefers it.
	Files []SourceFile `json:"files,omitempty"`
}

// FactoryRepositorySpec is one factory.spec.repositories entry.
// CeilingCents is the optional per-repo hard stop.
type FactoryRepositorySpec struct {
	RepositoryRef `json:",inline"`
	// +kubebuilder:validation:Minimum=1
	CeilingCents int64 `json:"ceilingCents,omitempty"`
}

// RepositoryRefs returns the embedded repository identity without the per-repo ceiling.
func RepositoryRefs(in []FactoryRepositorySpec) []RepositoryRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]RepositoryRef, len(in))
	for i := range in {
		out[i] = in[i].RepositoryRef
	}
	return out
}

// AgentDefaultsSpec is factory.spec.agentDefaults.
type AgentDefaultsSpec struct {
	Model              string                   `json:"model,omitempty"`
	Harness            *HarnessSpec             `json:"harness,omitempty"`
	Runner             string                   `json:"runner,omitempty"`
	Pool               string                   `json:"pool,omitempty"`
	PoolRef            *LocalRef                `json:"poolRef,omitempty"`
	ComputerUseModel   string                   `json:"computerUseModel,omitempty"`
	Secrets            []string                 `json:"secrets,omitempty"`
	MCPServers         map[string]MCPServerSpec `json:"mcpServers,omitempty"`
	WorkerHost         string                   `json:"workerHost,omitempty"`
	CredentialStrategy string                   `json:"credentialStrategy,omitempty"`
	IdleTimeoutMinutes int32                    `json:"idleTimeoutMinutes,omitempty"`
	MaxTurns           int32                    `json:"maxTurns,omitempty"`
}

// InferenceSpec is factory.spec.inference. CredentialRef is a name.
type InferenceSpec struct {
	Default   InferenceDefaultSpec             `json:"default,omitempty"`
	Providers map[string]InferenceProviderSpec `json:"providers,omitempty"`
	Rates     *SpendRateSpec                   `json:"rates,omitempty"`
}

// RoleSourceSpec is the role-file layout declared on the factory.
type RoleSourceSpec struct {
	Repo   RepositoryRef    `json:"repo,omitempty"`
	Roster RosterSourceSpec `json:"roster,omitempty"`
	Seats  SeatFileSpec     `json:"seats,omitempty"`
}

// RosterSourceSpec is the roster path inside the role repo.
type RosterSourceSpec struct {
	Path string `json:"path,omitempty"`
}

// SeatFileSpec is the seat path template and ordered file list.
type SeatFileSpec struct {
	PathTemplate string `json:"pathTemplate,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Files []string `json:"files,omitempty"`
}

// NativeSpec is the hierarchy limit and seat selection.
type NativeSpec struct {
	Limits      NativeLimitSpec `json:"limits,omitempty"`
	Root        string          `json:"root,omitempty"`
	Implementer string          `json:"implementer,omitempty"`
	Reviewer    string          `json:"reviewer,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	Seats []string `json:"seats,omitempty"`
}

// NativeLimitSpec is maxDepth and absoluteMaxDepth. Each is 1..8.
type NativeLimitSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8
	MaxDepth int32 `json:"maxDepth,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8
	AbsoluteMaxDepth int32 `json:"absoluteMaxDepth,omitempty"`
}

// PublishSpec is the draft-publication declaration.
type PublishSpec struct {
	Enabled   bool           `json:"enabled,omitempty"`
	Repo      *RepositoryRef `json:"repo,omitempty"`
	IssueKey  string         `json:"issueKey,omitempty"`
	RefPrefix string         `json:"refPrefix,omitempty"`
	Title     string         `json:"title,omitempty"`
	Body      string         `json:"body,omitempty"`
}

// EvidenceSpec is the oracle package frozen on the definition.
type EvidenceSpec struct {
	Package  string `json:"package,omitempty"`
	Mutation string `json:"mutation,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Sentinel string `json:"sentinel,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Break string `json:"break,omitempty"`
}

// BudgetSpec is the factory-wide hard stop.
type BudgetSpec struct {
	// +kubebuilder:validation:Minimum=1
	CeilingCents int64 `json:"ceilingCents,omitempty"`
}

// SpendRateSpec is the spend price document on inference.rates.
type SpendRateSpec struct {
	// +kubebuilder:validation:Minimum=0
	PromptByteCap int64 `json:"prompt_byte_cap,omitempty"`
	// +kubebuilder:validation:Minimum=1
	MaxTokens int64           `json:"max_tokens,omitempty"`
	Models    []ModelRateSpec `json:"models,omitempty"`
}

// ModelRateSpec is one model in inference.rates. Costs are decimal USD per token.
type ModelRateSpec struct {
	Name                        string   `json:"name,omitempty"`
	InputCostPerToken           string   `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken          string   `json:"output_cost_per_token,omitempty"`
	CacheCreationInputTokenCost string   `json:"cache_creation_input_token_cost,omitempty"`
	InputAbove                  []string `json:"input_above,omitempty"`
	OutputAbove                 []string `json:"output_above,omitempty"`
}

// InferenceDefaultSpec selects the default provider.
type InferenceDefaultSpec struct {
	Provider      string `json:"provider,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
}

// InferenceProviderSpec is one named provider. CredentialRef is a name.
type InferenceProviderSpec struct {
	Kind          string `json:"kind,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
	Region        string `json:"region,omitempty"`
	RoleArn       string `json:"roleArn,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
}

// PipelineSpec is the declarative stage machine.
type PipelineSpec struct {
	Stages        []string                `json:"stages,omitempty"`
	Agents        map[string]string       `json:"agents,omitempty"`
	Skip          map[string]string       `json:"skip,omitempty"`
	Approvals     map[string]ApprovalSpec `json:"approvals,omitempty"`
	RevisionLimit int32                   `json:"revisionLimit,omitempty"`
}

// ApprovalSpec is one pipeline.approvals entry.
type ApprovalSpec struct {
	Required  bool   `json:"required"`
	Approvers string `json:"approvers,omitempty"`
}

// SelfImprovementSpec is factory.spec.selfImprovement.
// reviewerEmails is non-empty if and only if reviewerType is custom.
//
// +kubebuilder:validation:XValidation:rule="(has(self.reviewerType) && self.reviewerType == 'custom') == (has(self.reviewerEmails) && self.reviewerEmails.exists(e, size(e.trim()) > 0))",message="reviewerEmails must be non-empty if and only if reviewerType is custom"
type SelfImprovementSpec struct {
	FailedRunThreshold int32 `json:"failedRunThreshold,omitempty"`
	StaleFailureDays   int32 `json:"staleFailureDays,omitempty"`
	// +kubebuilder:validation:Enum=admins;team;custom;none
	ReviewerType string `json:"reviewerType,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=320
	ReviewerEmails []string `json:"reviewerEmails,omitempty"`
}

// ScorerDefaultsSpec is factory.spec.scorerDefaults.
type ScorerDefaultsSpec struct {
	Runner     string                   `json:"runner,omitempty"`
	Model      string                   `json:"model,omitempty"`
	Secrets    []string                 `json:"secrets,omitempty"`
	MCPServers map[string]MCPServerSpec `json:"mcpServers,omitempty"`
}

// FactoryStatus is the operator's view of a Factory.
type FactoryStatus struct {
	Phase              string                `json:"phase,omitempty"`
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Resources          FactoryResourceCounts `json:"resources,omitempty"`
	Conditions         []metav1.Condition    `json:"conditions,omitempty"`
}

// FactoryResourceCounts is status.resources.
type FactoryResourceCounts struct {
	Agents      int32 `json:"agents,omitempty"`
	Automations int32 `json:"automations,omitempty"`
	Runners     int32 `json:"runners,omitempty"`
	Scorers     int32 `json:"scorers,omitempty"`
}

// +kubebuilder:object:root=true
type FactoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Factory `json:"items"`
}
