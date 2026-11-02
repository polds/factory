package workerpool

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/polds/factory/internal/operator"
)

var k8sClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join(wd, "..", "..", "..", "deploy", "helm", "factory", "crds")},
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: operator.Scheme})
	if err != nil {
		_ = env.Stop()
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func requireEnvtest(t *testing.T) {
	t.Helper()
	if k8sClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset")
	}
}
