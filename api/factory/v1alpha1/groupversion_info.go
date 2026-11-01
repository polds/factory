// Package v1alpha1 is the factory.dev/v1alpha1 Kubernetes API.
//
// The eleven kinds mirror a factory definition tree. spec.files on the nine
// definition kinds is the lossless source factoryctl convert writes back.
// Structured fields are the docs/10 projection.
//
// +kubebuilder:object:generate=true
// +groupName=factory.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SchemeGroupVersion is the factory.dev API group and version.
var SchemeGroupVersion = schema.GroupVersion{Group: "factory.dev", Version: "v1alpha1"}

// Resource returns a GroupResource for the plural name.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

var (
	// SchemeBuilder registers the factory.dev kinds.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds the factory.dev kinds to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion,
		&Factory{}, &FactoryList{},
		&FactoryAgent{}, &FactoryAgentList{},
		&Automation{}, &AutomationList{},
		&Runner{}, &RunnerList{},
		&ModelRouter{}, &ModelRouterList{},
		&Scorer{}, &ScorerList{},
		&Skill{}, &SkillList{},
		&FactoryWebhook{}, &FactoryWebhookList{},
		&BenchmarkSuite{}, &BenchmarkSuiteList{},
		&AgentRun{}, &AgentRunList{},
		&WorkerPool{}, &WorkerPoolList{},
	)
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
}
