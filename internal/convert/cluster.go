package convert

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// definitionResources are the nine kinds convert reads and writes.
// AgentRun and WorkerPool are not in this list.
var definitionResources = []schema.GroupVersionResource{
	{Group: "factory.dev", Version: "v1alpha1", Resource: "factories"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "runners"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "modelrouters"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "factoryagents"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "automations"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "scorers"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "skills"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "factorywebhooks"},
	{Group: "factory.dev", Version: "v1alpha1", Resource: "benchmarksuites"},
}

// FromCluster reads the nine definition kinds from namespace and writes a
// definition tree to dest. It uses spec.files when the resources were
// produced by DirToManifests, and synthesizes files otherwise.
func FromCluster(ctx context.Context, cfg *rest.Config, namespace, dest string) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	if cfg == nil {
		return errors.New("rest config is required")
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return redactErr(err)
	}
	var objs []map[string]any
	for _, gvr := range definitionResources {
		list, err := dc.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return redactErr(fmt.Errorf("list %s: %w", gvr.Resource, err))
		}
		for i := range list.Items {
			objs = append(objs, list.Items[i].Object)
		}
	}
	cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	cmList, err := dc.Resource(cmGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return redactErr(err)
	}
	configMaps := map[string]map[string]string{}
	for i := range cmList.Items {
		name, data := configMapData(cmList.Items[i].Object)
		if name != "" {
			configMaps[name] = data
		}
	}
	var files []SourceFile
	for _, obj := range objs {
		got, err := filesFromDoc(kindOf(obj), obj, configMaps)
		if err != nil {
			return redactErr(err)
		}
		files = append(files, got...)
	}
	if len(files) == 0 {
		return fmt.Errorf("no definition resources in namespace %s", namespace)
	}
	return WriteFiles(dest, files)
}
