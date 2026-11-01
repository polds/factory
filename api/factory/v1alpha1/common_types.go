package v1alpha1

import apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

// LocalRef names a resource in the same namespace (D12).
type LocalRef struct {
	Name string `json:"name"`
}

// SourceFile is one definition-tree file.
//
// Content is what factoryctl convert writes back. Inline credential values
// are removed before Content is stored. secretRefs, credentialRef, and
// secretName stay names.
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ConfigMapKeyRef selects a key in a ConfigMap.
type ConfigMapKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// ConfigMapSource is the instructionsFrom / promptFrom / rubricFrom shape.
type ConfigMapSource struct {
	ConfigMapKeyRef ConfigMapKeyRef `json:"configMapKeyRef"`
}

// RepositoryRef is a code-forge repository.
type RepositoryRef struct {
	CodeForge string `json:"codeForge,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Name      string `json:"name,omitempty"`
	Deferred  bool   `json:"deferred,omitempty"`
}

// SecretRef names a secret. Name is the secret's name. As is the env var
// that name is exposed as. Neither field holds secret material.
type SecretRef struct {
	Name string `json:"name"`
	As   string `json:"as,omitempty"`
}

// HarnessSpec is an explicit harness. It is mutually exclusive with model.
type HarnessSpec struct {
	Type  string           `json:"type,omitempty"`
	Model string           `json:"model,omitempty"`
	Auth  *HarnessAuthSpec `json:"auth,omitempty"`
}

// HarnessAuthSpec names a harness credential. SecretName is a name.
type HarnessAuthSpec struct {
	Source     string `json:"source,omitempty"`
	SecretName string `json:"secretName,omitempty"`
}

// MCPServerSpec is one MCP server. Header and env values are either a
// secretKeyRef object or a {{SECRET_NAME}} string, never credential material.
type MCPServerSpec struct {
	URL     string   `json:"url,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Headers map[string]apiextensionsv1.JSON `json:"headers,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Env map[string]apiextensionsv1.JSON `json:"env,omitempty"`
}

// PlatformSpec is a runner or pool platform.
type PlatformSpec struct {
	OS    string             `json:"os,omitempty"`
	Arch  string             `json:"arch,omitempty"`
	Linux *LinuxPlatformSpec `json:"linux,omitempty"`
}

// LinuxPlatformSpec is platform.linux. RegistryCredentialSecretName is a name.
type LinuxPlatformSpec struct {
	DockerImage                  string `json:"dockerImage,omitempty"`
	RegistryCredentialSecretName string `json:"registryCredentialSecretName,omitempty"`
}

// InstanceShapeSpec is a runner size hint.
type InstanceShapeSpec struct {
	VCPUs    int32 `json:"vcpus,omitempty"`
	MemoryGB int32 `json:"memoryGb,omitempty"`
}
