package operator

import (
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
)

// Scheme has the factory.dev kinds and core Kubernetes kinds.
var Scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(Scheme))
	utilruntime.Must(admissionv1.AddToScheme(Scheme))
	utilruntime.Must(factoryv1.AddToScheme(Scheme))
}
