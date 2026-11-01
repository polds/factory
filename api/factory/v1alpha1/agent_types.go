package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// FactoryAgent is one agents/<name>/agent.md.
// Exactly one of spec.model and spec.harness is set.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type FactoryAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FactoryAgentSpec   `json:"spec,omitempty"`
	Status            FactoryAgentStatus `json:"status,omitempty"`
}

// FactoryAgentSpec is the desired agent.
//
// +kubebuilder:validation:XValidation:rule="(has(self.model) && size(self.model) > 0 ? 1 : 0) + (has(self.harness) ? 1 : 0) == 1",message="exactly one of model or harness must be set"
type FactoryAgentSpec struct {
	FactoryRef *LocalRef `json:"factoryRef,omitempty"`
	// +kubebuilder:validation:Enum=CUSTOM;FOREMAN;TRIAGE;SPEC;IMPLEMENT;REVIEW;VERIFY
	AgentType          string                   `json:"agentType,omitempty"`
	Description        string                   `json:"description,omitempty"`
	Model              string                   `json:"model,omitempty"`
	Harness            *HarnessSpec             `json:"harness,omitempty"`
	SpawnableBy        []string                 `json:"spawnableBy,omitempty"`
	IdleTimeoutMinutes int32                    `json:"idleTimeoutMinutes,omitempty"`
	Runner             string                   `json:"runner,omitempty"`
	Pool               string                   `json:"pool,omitempty"`
	PoolRef            *LocalRef                `json:"poolRef,omitempty"`
	ComputerUseModel   string                   `json:"computerUseModel,omitempty"`
	CredentialStrategy string                   `json:"credentialStrategy,omitempty"`
	MaxTurns           int32                    `json:"maxTurns,omitempty"`
	WorkerHost         string                   `json:"workerHost,omitempty"`
	RequiredRepos      []RepositoryRef          `json:"requiredRepos,omitempty"`
	Secrets            []string                 `json:"secrets,omitempty"`
	SecretRefs         []SecretRef              `json:"secretRefs,omitempty"`
	MCPServers         map[string]MCPServerSpec `json:"mcpServers,omitempty"`
	Instructions       string                   `json:"instructions,omitempty"`
	InstructionsFrom   *ConfigMapSource         `json:"instructionsFrom,omitempty"`
	Files              []SourceFile             `json:"files,omitempty"`
}

// FactoryAgentStatus publishes the inherited config.
type FactoryAgentStatus struct {
	Phase           string             `json:"phase,omitempty"`
	EffectiveConfig *EffectiveConfig   `json:"effectiveConfig,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// EffectiveConfig is status.effectiveConfig.
type EffectiveConfig struct {
	Harness *HarnessSpec `json:"harness,omitempty"`
	Model   string       `json:"model,omitempty"`
	Pool    string       `json:"pool,omitempty"`
}

// +kubebuilder:object:root=true
type FactoryAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FactoryAgent `json:"items"`
}
