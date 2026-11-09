package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/convert"
)

const (
	annGeneration = "factory.dev/definition-generation"
	annHash       = "factory.dev/definition-hash"
)

// pushGeneration is the generation sent to factoryd.
// A child change does not bump Factory.metadata.generation, so a changed
// bundle whose stored generation is already at the Factory generation
// advances by one. An unchanged bundle resends the stored generation.
func pushGeneration(factoryGen, prev int64, changed bool) int64 {
	if !changed {
		if prev > 0 {
			return prev
		}
		return factoryGen
	}
	if prev >= factoryGen {
		return prev + 1
	}
	if factoryGen < 1 {
		return prev + 1
	}
	return factoryGen
}

func annInt(obj client.Object, key string) int64 {
	if obj.GetAnnotations() == nil {
		return 0
	}
	n, err := strconv.ParseInt(obj.GetAnnotations()[key], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func bundleHash(files []api.DefinitionFile) string {
	cp := append([]api.DefinitionFile(nil), files...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Path < cp[j].Path })
	h := sha256.New()
	for _, file := range cp {
		_, _ = h.Write([]byte(file.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(file.Content))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type bundleResult struct {
	files  []api.DefinitionFile
	counts factoryv1.FactoryResourceCounts
}

// addBundleFiles merges files into the bundle. The same path and content is
// kept once. A repeated path with different content is an error.
func addBundleFiles(out *bundleResult, seen map[string]string, files []api.DefinitionFile) error {
	for _, file := range files {
		if prev, ok := seen[file.Path]; ok && prev != file.Content {
			return fmt.Errorf("duplicate definition path %s", file.Path)
		}
		if _, ok := seen[file.Path]; ok {
			continue
		}
		seen[file.Path] = file.Content
		out.files = append(out.files, file)
	}
	return nil
}

func (r *FactoryReconciler) bundle(ctx context.Context, factory *factoryv1.Factory) (bundleResult, error) {
	var out bundleResult
	seen := map[string]string{}
	add := func(files []api.DefinitionFile) error {
		return addBundleFiles(&out, seen, files)
	}
	files, err := filesFromObject(factory, factory.Spec.Files)
	if err != nil {
		return out, err
	}
	if err := add(files); err != nil {
		return out, err
	}
	var agentList factoryv1.FactoryAgentList
	agents, err := ownedItems(ctx, r, factory, &agentList, &agentList.Items, func(item *factoryv1.FactoryAgent) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return out, err
	}
	out.counts.Agents = int32(len(agents))
	for i := range agents {
		files, err = filesFromObject(&agents[i], agents[i].Spec.Files)
		if err != nil {
			return out, err
		}
		if err := add(files); err != nil {
			return out, err
		}
	}
	var automationList factoryv1.AutomationList
	autos, err := ownedItems(ctx, r, factory, &automationList, &automationList.Items, func(item *factoryv1.Automation) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return out, err
	}
	out.counts.Automations = int32(len(autos))
	for i := range autos {
		files, err = filesFromObject(&autos[i], autos[i].Spec.Files)
		if err != nil {
			return out, err
		}
		if err := add(files); err != nil {
			return out, err
		}
	}
	var runnerList factoryv1.RunnerList
	runners, err := ownedItems(ctx, r, factory, &runnerList, &runnerList.Items, func(item *factoryv1.Runner) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return out, err
	}
	out.counts.Runners = int32(len(runners))
	for i := range runners {
		files, err = filesFromObject(&runners[i], runners[i].Spec.Files)
		if err != nil {
			return out, err
		}
		if err := add(files); err != nil {
			return out, err
		}
	}
	var scorerList factoryv1.ScorerList
	scorers, err := ownedItems(ctx, r, factory, &scorerList, &scorerList.Items, func(item *factoryv1.Scorer) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return out, err
	}
	out.counts.Scorers = int32(len(scorers))
	for i := range scorers {
		files, err = filesFromObject(&scorers[i], scorers[i].Spec.Files)
		if err != nil {
			return out, err
		}
		if err := add(files); err != nil {
			return out, err
		}
	}
	if err := r.addRefKind(ctx, factory, &out, seen); err != nil {
		return out, err
	}
	sort.Slice(out.files, func(i, j int) bool { return out.files[i].Path < out.files[j].Path })
	return out, nil
}

func (r *FactoryReconciler) addRefKind(ctx context.Context, factory *factoryv1.Factory, out *bundleResult, seen map[string]string) error {
	addObj := func(obj client.Object, files []factoryv1.SourceFile) error {
		got, err := filesFromObject(obj, files)
		if err != nil {
			return err
		}
		return addBundleFiles(out, seen, got)
	}
	var routerList factoryv1.ModelRouterList
	routers, err := ownedItems(ctx, r, factory, &routerList, &routerList.Items, func(item *factoryv1.ModelRouter) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return err
	}
	for i := range routers {
		if err := addObj(&routers[i], routers[i].Spec.Files); err != nil {
			return err
		}
	}
	var skillList factoryv1.SkillList
	skills, err := ownedItems(ctx, r, factory, &skillList, &skillList.Items, func(item *factoryv1.Skill) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return err
	}
	for i := range skills {
		if err := addObj(&skills[i], skills[i].Spec.Files); err != nil {
			return err
		}
	}
	var hookList factoryv1.FactoryWebhookList
	hooks, err := ownedItems(ctx, r, factory, &hookList, &hookList.Items, func(item *factoryv1.FactoryWebhook) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return err
	}
	for i := range hooks {
		if err := addObj(&hooks[i], hooks[i].Spec.Files); err != nil {
			return err
		}
	}
	var benchList factoryv1.BenchmarkSuiteList
	benches, err := ownedItems(ctx, r, factory, &benchList, &benchList.Items, func(item *factoryv1.BenchmarkSuite) *factoryv1.LocalRef {
		return item.Spec.FactoryRef
	})
	if err != nil {
		return err
	}
	for i := range benches {
		if err := addObj(&benches[i], benches[i].Spec.Files); err != nil {
			return err
		}
	}
	return nil
}

func filesFromObject(obj client.Object, files []factoryv1.SourceFile) ([]api.DefinitionFile, error) {
	if len(files) > 0 {
		out := make([]api.DefinitionFile, 0, len(files))
		for _, file := range files {
			out = append(out, api.DefinitionFile{Path: file.Path, Content: file.Content})
		}
		return out, nil
	}
	return synthesize(obj)
}

// synthesize rebuilds definition files from typed fields.
// Objects that already carry spec.files never reach this function.
func synthesize(obj client.Object) ([]api.DefinitionFile, error) {
	raw, err := manifestYAML(obj)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "factory-synth-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := convert.ManifestsToDir(raw, dir); err != nil {
		return nil, err
	}
	got, err := convert.ReadFiles(dir)
	if err != nil {
		return nil, err
	}
	out := make([]api.DefinitionFile, 0, len(got))
	for _, file := range got {
		out = append(out, api.DefinitionFile{Path: file.Path, Content: file.Content})
	}
	return out, nil
}

func manifestYAML(obj client.Object) ([]byte, error) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return nil, err
	}
	delete(doc, "status")
	if meta, ok := doc["metadata"].(map[string]any); ok {
		delete(meta, "resourceVersion")
		delete(meta, "uid")
		delete(meta, "managedFields")
		delete(meta, "generation")
		delete(meta, "creationTimestamp")
	}
	gvks, _, err := Scheme.ObjectKinds(obj)
	if err != nil {
		return nil, err
	}
	doc["apiVersion"] = gvks[0].GroupVersion().String()
	doc["kind"] = gvks[0].Kind
	return yaml.Marshal(doc)
}

func owns(factory *factoryv1.Factory, ref *factoryv1.LocalRef, r *FactoryReconciler, ctx context.Context) bool {
	if ref != nil && ref.Name != "" {
		return ref.Name == factory.Name
	}
	return soleFactory(ctx, r, factory.Namespace, factory.Name)
}

func soleFactory(ctx context.Context, c client.Client, namespace, name string) bool {
	var list factoryv1.FactoryList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return false
	}
	return len(list.Items) == 1 && list.Items[0].Name == name
}
