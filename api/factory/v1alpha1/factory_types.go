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
type FactorySpec struct {
	SchemaVersion      string                   `json:"schemaVersion,omitempty"`
	DisplayName        string                   `json:"displayName,omitempty"`
	Description        string                   `json:"description,omitempty"`
	Alias              string                   `json:"alias,omitempty"`
	CodeForges         []string                 `json:"codeForges,omitempty"`
	Repositories       []RepositoryRef          `json:"repositories,omitempty"`
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
