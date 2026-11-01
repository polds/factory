package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Automation is one automations/<name>/automation.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type Automation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AutomationSpec   `json:"spec,omitempty"`
	Status            AutomationStatus `json:"status,omitempty"`
}

// AutomationSpec is the desired automation.
type AutomationSpec struct {
	FactoryRef    *LocalRef                `json:"factoryRef,omitempty"`
	Enabled       bool                     `json:"enabled"`
	AgentRef      *LocalRef                `json:"agentRef,omitempty"`
	DisplayName   string                   `json:"displayName,omitempty"`
	Triggers      []TriggerSpec            `json:"triggers,omitempty"`
	Model         string                   `json:"model,omitempty"`
	Harness       *HarnessSpec             `json:"harness,omitempty"`
	Runner        string                   `json:"runner,omitempty"`
	Pool          string                   `json:"pool,omitempty"`
	RequiredRepos []RepositoryRef          `json:"requiredRepos,omitempty"`
	Secrets       []string                 `json:"secrets,omitempty"`
	SecretRefs    []SecretRef              `json:"secretRefs,omitempty"`
	MCPServers    map[string]MCPServerSpec `json:"mcpServers,omitempty"`
	Prompt        string                   `json:"prompt,omitempty"`
	PromptFrom    *ConfigMapSource         `json:"promptFrom,omitempty"`
	Files         []SourceFile             `json:"files,omitempty"`
}

// TriggerSpec is one automation trigger. Filter keeps the definition's shape.
type TriggerSpec struct {
	Provider string `json:"provider,omitempty"`
	Event    string `json:"event,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Filter   *apiextensionsv1.JSON `json:"filter,omitempty"`
	Schedule *ScheduleSpec         `json:"schedule,omitempty"`
}

// ScheduleSpec is a cron trigger.
type ScheduleSpec struct {
	Name string `json:"name,omitempty"`
	Cron string `json:"cron,omitempty"`
}

// AutomationStatus is reserved for the operator.
type AutomationStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type AutomationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Automation `json:"items"`
}

// Runner is one runners/<name>.yaml.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type Runner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RunnerSpec   `json:"spec,omitempty"`
	Status            RunnerStatus `json:"status,omitempty"`
}

// RunnerSpec is the desired runner.
type RunnerSpec struct {
	FactoryRef                     *LocalRef          `json:"factoryRef,omitempty"`
	Description                    string             `json:"description,omitempty"`
	SetupCommands                  []string           `json:"setupCommands,omitempty"`
	InstanceShape                  *InstanceShapeSpec `json:"instanceShape,omitempty"`
	Platform                       *PlatformSpec      `json:"platform,omitempty"`
	FailureSessionRetentionMinutes int32              `json:"failureSessionRetentionMinutes,omitempty"`
	Files                          []SourceFile       `json:"files,omitempty"`
}

// RunnerStatus is reserved for the operator.
type RunnerStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type RunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Runner `json:"items"`
}

// ModelRouter is one routers/<name>.yaml.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type ModelRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelRouterSpec   `json:"spec,omitempty"`
	Status            ModelRouterStatus `json:"status,omitempty"`
}

// ModelRouterSpec is the desired router.
type ModelRouterSpec struct {
	FactoryRef  *LocalRef `json:"factoryRef,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	// +kubebuilder:validation:Enum=prompt;complexity
	Type       string            `json:"type,omitempty"`
	Default    string            `json:"default,omitempty"`
	Routes     []RouterRouteSpec `json:"routes,omitempty"`
	Complexity map[string]string `json:"complexity,omitempty"`
	Files      []SourceFile      `json:"files,omitempty"`
}

// RouterRouteSpec is one prompt-router choice.
type RouterRouteSpec struct {
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
}

// ModelRouterStatus is reserved for the operator.
type ModelRouterStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ModelRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelRouter `json:"items"`
}

// Scorer is one scorers/<name>/scorer.md.
// At least one label falls on each side of passingScore.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type Scorer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ScorerSpec   `json:"spec,omitempty"`
	Status            ScorerStatus `json:"status,omitempty"`
}

// ScorerSpec is the desired scorer.
//
// +kubebuilder:validation:XValidation:rule="has(self.labels) && self.labels.exists(l, l.score >= (has(self.passingScore) ? self.passingScore : 0.0)) && self.labels.exists(l, l.score < (has(self.passingScore) ? self.passingScore : 0.0))",message="at least one scorer label must fall on each side of passingScore"
type ScorerSpec struct {
	FactoryRef  *LocalRef         `json:"factoryRef,omitempty"`
	Description string            `json:"description,omitempty"`
	Agents      []ScorerAgentSpec `json:"agents,omitempty"`
	Output      string            `json:"output,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	Labels          []ScorerLabelSpec `json:"labels,omitempty"`
	PassingScore    float64           `json:"passingScore,omitempty"`
	SamplingRate    int32             `json:"samplingRate,omitempty"`
	Model           string            `json:"model,omitempty"`
	SelfImprovement bool              `json:"selfImprovement,omitempty"`
	Rubric          string            `json:"rubric,omitempty"`
	RubricFrom      *ConfigMapSource  `json:"rubricFrom,omitempty"`
	Files           []SourceFile      `json:"files,omitempty"`
}

// ScorerAgentSpec names an agent a scorer samples.
type ScorerAgentSpec struct {
	Name               string `json:"name"`
	IncludeDescendants bool   `json:"includeDescendants,omitempty"`
}

// ScorerLabelSpec is one classification label.
type ScorerLabelSpec struct {
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:Required
	Score float64 `json:"score"`
}

// ScorerStatus is reserved for the operator.
type ScorerStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ScorerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Scorer `json:"items"`
}

// Skill is one skills/<name>/SKILL.md, or an agent-local skill.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type Skill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SkillSpec   `json:"spec,omitempty"`
	Status            SkillStatus `json:"status,omitempty"`
}

// SkillSpec is the desired skill. AgentRef is set for an agent-local skill.
type SkillSpec struct {
	FactoryRef       *LocalRef        `json:"factoryRef,omitempty"`
	AgentRef         *LocalRef        `json:"agentRef,omitempty"`
	Description      string           `json:"description,omitempty"`
	Instructions     string           `json:"instructions,omitempty"`
	InstructionsFrom *ConfigMapSource `json:"instructionsFrom,omitempty"`
	Files            []SourceFile     `json:"files,omitempty"`
}

// SkillStatus is reserved for the operator.
type SkillStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type SkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Skill `json:"items"`
}

// FactoryWebhook is one webhooks/<name>.yaml.
// signatureScheme is set if and only if authMode is signature.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type FactoryWebhook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FactoryWebhookSpec   `json:"spec,omitempty"`
	Status            FactoryWebhookStatus `json:"status,omitempty"`
}

// FactoryWebhookSpec is the desired webhook. SecretName is a name.
//
// +kubebuilder:validation:XValidation:rule="(has(self.authMode) && self.authMode == 'signature') == (has(self.signatureScheme) && size(self.signatureScheme) > 0)",message="signatureScheme is required if and only if authMode is signature"
type FactoryWebhookSpec struct {
	FactoryRef *LocalRef `json:"factoryRef,omitempty"`
	// +kubebuilder:validation:Enum=token;url_token;signature
	AuthMode         string       `json:"authMode,omitempty"`
	SignatureScheme  string       `json:"signatureScheme,omitempty"`
	SecretName       string       `json:"secretName,omitempty"`
	DeliveryIDHeader string       `json:"deliveryIdHeader,omitempty"`
	Enabled          bool         `json:"enabled"`
	Files            []SourceFile `json:"files,omitempty"`
}

// FactoryWebhookStatus is reserved for the operator.
type FactoryWebhookStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type FactoryWebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FactoryWebhook `json:"items"`
}

// BenchmarkSuite is benchmarks/<suite>/, including its task files.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type BenchmarkSuite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BenchmarkSuiteSpec   `json:"spec,omitempty"`
	Status            BenchmarkSuiteStatus `json:"status,omitempty"`
}

// BenchmarkSuiteSpec is the desired suite.
type BenchmarkSuiteSpec struct {
	FactoryRef     *LocalRef                    `json:"factoryRef,omitempty"`
	Description    string                       `json:"description,omitempty"`
	Agent          string                       `json:"agent,omitempty"`
	Configurations []BenchmarkConfigurationSpec `json:"configurations,omitempty"`
	Tasks          []string                     `json:"tasks,omitempty"`
	Files          []SourceFile                 `json:"files,omitempty"`
}

// BenchmarkConfigurationSpec is one suite configuration.
type BenchmarkConfigurationSpec struct {
	Name          string                 `json:"name,omitempty"`
	DefinitionRef string                 `json:"definitionRef,omitempty"`
	Agents        []BenchmarkBindingSpec `json:"agents,omitempty"`
}

// BenchmarkBindingSpec binds an agent to exactly one of model or harness.
//
// +kubebuilder:validation:XValidation:rule="(has(self.model) && size(self.model) > 0 ? 1 : 0) + (has(self.harness) ? 1 : 0) == 1",message="exactly one of model or harness must be set"
type BenchmarkBindingSpec struct {
	Agent   string       `json:"agent,omitempty"`
	Model   string       `json:"model,omitempty"`
	Harness *HarnessSpec `json:"harness,omitempty"`
}

// BenchmarkSuiteStatus is reserved for the operator.
type BenchmarkSuiteStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type BenchmarkSuiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BenchmarkSuite `json:"items"`
}
