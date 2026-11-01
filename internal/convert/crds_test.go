package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	syaml "sigs.k8s.io/yaml"
)

func TestEmbeddedCRDsMatchHelm(t *testing.T) {
	root := moduleRoot(t)
	helm := filepath.Join(root, "deploy", "helm", "factory", "crds")
	names, err := crdNames()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(helm)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("helm CRDs %d, embedded %d", len(entries), len(names))
	}
	for _, name := range names {
		want, err := os.ReadFile(filepath.Join(helm, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := crdFS.ReadFile("crds/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s differs from the Helm CRD", name)
		}
	}
	printed, err := CRDYAML()
	if err != nil {
		t.Fatal(err)
	}
	if len(printed) == 0 || !strings.Contains(string(printed), "kind: CustomResourceDefinition") {
		t.Fatal("CRDYAML is empty")
	}
}

func TestApplyCRDs(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; envtest assets are not installed")
	}
	if _, err := os.Stat(filepath.Join(assets, "kube-apiserver")); err != nil {
		t.Skip("envtest assets are absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := &envtest.Environment{BinaryAssetsDirectory: assets}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	if err := ApplyCRDs(ctx, cfg, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if err := ApplyCRDs(ctx, cfg, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names, err := crdNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range names {
		raw, err := crdFS.ReadFile("crds/" + file)
		if err != nil {
			t.Fatal(err)
		}
		body, err := syaml.YAMLToJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		name, err := crdObjectName(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := waitEstablished(ctx, dc, name); err != nil {
			t.Fatal(err)
		}
	}
}

func waitEstablished(ctx context.Context, dc dynamic.Interface, name string) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		obj, err := dc.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, cond := range conds {
				m, _ := cond.(map[string]any)
				if m["type"] == "Established" && m["status"] == "True" {
					return nil
				}
			}
		} else {
			last = err
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("CRD %s was not established: %w", name, last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
