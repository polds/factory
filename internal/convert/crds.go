package convert

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	syaml "sigs.k8s.io/yaml"
)

// CRDs are embedded because go:embed cannot reach deploy/helm/factory/crds.
// scripts/ci/crd-drift.sh checks this copy against a fresh controller-gen run.
// Helm installs CRDs and does not upgrade them; factoryctl crds apply is the
// upgrade path.

//go:embed crds/*.yaml
var crdFS embed.FS

var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// CRDYAML returns every embedded CRD as one multi-document YAML stream,
// in filename order.
func CRDYAML() ([]byte, error) {
	names, err := crdNames()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, name := range names {
		raw, err := crdFS.ReadFile("crds/" + name)
		if err != nil {
			return nil, err
		}
		buf.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// ApplyCRDs server-side applies the embedded CRDs. dryRun sets
// metav1.DryRunAll and does not persist the resources.
func ApplyCRDs(ctx context.Context, cfg *rest.Config, dryRun bool) error {
	if cfg == nil {
		return fmt.Errorf("rest config is required")
	}
	names, err := crdNames()
	if err != nil {
		return err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return redactErr(err)
	}
	force := true
	var dry []string
	if dryRun {
		dry = []string{metav1.DryRunAll}
	}
	for _, name := range names {
		raw, err := crdFS.ReadFile("crds/" + name)
		if err != nil {
			return err
		}
		body, err := syaml.YAMLToJSON(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		crdName, err := crdObjectName(body)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		_, err = dc.Resource(crdGVR).Patch(ctx, crdName, types.ApplyPatchType, body, metav1.PatchOptions{
			FieldManager: "factoryctl",
			Force:        &force,
			DryRun:       dry,
		})
		if err != nil {
			return redactErr(fmt.Errorf("apply %s: %w", crdName, err))
		}
	}
	return nil
}

func crdObjectName(body []byte) (string, error) {
	var meta struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", err
	}
	if meta.Metadata.Name == "" {
		return "", fmt.Errorf("CRD metadata.name is empty")
	}
	return meta.Metadata.Name, nil
}

func crdNames() ([]string, error) {
	entries, err := fs.ReadDir(crdFS, "crds")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no embedded CRDs")
	}
	sort.Strings(names)
	return names, nil
}
