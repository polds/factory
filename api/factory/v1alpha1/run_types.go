package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AgentRun starts work. It is imperative and is never produced by convert.
// spec is immutable after create.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AgentRunSpec   `json:"spec,omitempty"`
	Status            AgentRunStatus `json:"status,omitempty"`
}

// AgentRunSpec is the work to start. factoryRef is same-namespace only.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type AgentRunSpec struct {
	FactoryRef *LocalRef `json:"factoryRef,omitempty"`
	AgentRef   *LocalRef `json:"agentRef,omitempty"`
	Prompt     string    `json:"prompt,omitempty"`
	// +kubebuilder:validation:Enum=interactive;pipeline;batch
	Priority       string       `json:"priority,omitempty"`
	PoolRef        *LocalRef    `json:"poolRef,omitempty"`
	ExternalRef    *ExternalRef `json:"externalRef,omitempty"`
	TimeoutSeconds int32        `json:"timeoutSeconds,omitempty"`
}

// ExternalRef is a provider id for an AgentRun. ID is an external identifier,
// not a credential.
type ExternalRef struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id,omitempty"`
}

// AgentRunStatus mirrors a control-plane run.
type AgentRunStatus struct {
	Phase      string              `json:"phase,omitempty"`
	RunID      string              `json:"runId,omitempty"`
	WorkItemID string              `json:"workItemId,omitempty"`
	WorkerID   string              `json:"workerId,omitempty"`
	Executions []AgentRunExecution `json:"executions,omitempty"`
	Conditions []metav1.Condition  `json:"conditions,omitempty"`
	Usage      *AgentRunUsage      `json:"usage,omitempty"`
	PRURLs     []string            `json:"prUrls,omitempty"`
}

// AgentRunExecution is one execution attempt.
type AgentRunExecution struct {
	ID      string `json:"id,omitempty"`
	Attempt int32  `json:"attempt,omitempty"`
	State   string `json:"state,omitempty"`
}

// AgentRunUsage is token and cost accounting. It holds no credentials.
type AgentRunUsage struct {
	InputTokens  int64 `json:"inputTokens,omitempty"`
	OutputTokens int64 `json:"outputTokens,omitempty"`
	CostCents    int64 `json:"costCents,omitempty"`
}

// +kubebuilder:object:root=true
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

// WorkerPool declares worker capacity. convert never emits it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type WorkerPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkerPoolSpec   `json:"spec,omitempty"`
	Status            WorkerPoolStatus `json:"status,omitempty"`
}

// WorkerPoolSpec is the desired pool.
type WorkerPoolSpec struct {
	Backend            string                 `json:"backend,omitempty"`
	Platform           *PlatformSpec          `json:"platform,omitempty"`
	Replicas           int32                  `json:"replicas,omitempty"`
	MaxConcurrentTasks int32                  `json:"maxConcurrentTasks,omitempty"`
	Labels             map[string]string      `json:"labels,omitempty"`
	HarnessImages      map[string]string      `json:"harnessImages,omitempty"`
	KubernetesBackend  *KubernetesBackendSpec `json:"kubernetesBackend,omitempty"`
}

// KubernetesBackendSpec is the pool's Kubernetes task backend.
type KubernetesBackendSpec struct {
	Namespace               string             `json:"namespace,omitempty"`
	DefaultImage            string             `json:"defaultImage,omitempty"`
	ImagePullPolicy         string             `json:"imagePullPolicy,omitempty"`
	ActiveDeadlineSeconds   int64              `json:"activeDeadlineSeconds,omitempty"`
	TTLSecondsAfterFinished int32              `json:"ttlSecondsAfterFinished,omitempty"`
	WorkspaceSizeLimit      string             `json:"workspaceSizeLimit,omitempty"`
	UnschedulableTimeout    string             `json:"unschedulableTimeout,omitempty"`
	ExtraAnnotations        map[string]string  `json:"extraAnnotations,omitempty"`
	PodTemplate             *WorkerPodTemplate `json:"podTemplate,omitempty"`
}

// WorkerPodTemplate is the subset of a pod template the example resources set.
type WorkerPodTemplate struct {
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	Containers         []WorkerContainer `json:"containers,omitempty"`
}

// WorkerContainer customizes the task container named "task".
type WorkerContainer struct {
	Name      string              `json:"name,omitempty"`
	Resources *ContainerResources `json:"resources,omitempty"`
}

// ContainerResources is requests and limits as quantity strings.
type ContainerResources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// WorkerPoolStatus is the operator's view of pool capacity.
type WorkerPoolStatus struct {
	ConnectedWorkers int32              `json:"connectedWorkers,omitempty"`
	InFlight         int32              `json:"inFlight,omitempty"`
	QueueDepth       int32              `json:"queueDepth,omitempty"`
	RejectRate1m     float64            `json:"rejectRate1m,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type WorkerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerPool `json:"items"`
}
