package convert

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/polds/factory/api/factory/v1alpha1"
	"github.com/polds/factory/internal/definition"
)

func project(tree *definition.Tree, files []SourceFile, opt Options) ([]any, error) {
	byPath := map[string]string{}
	for _, file := range files {
		byPath[file.Path] = file.Content
	}
	used := map[string]bool{}
	take := func(path string) []v1alpha1.SourceFile {
		content, ok := byPath[path]
		if !ok {
			return nil
		}
		used[path] = true
		return []v1alpha1.SourceFile{{Path: path, Content: content}}
	}
	takePrefix := func(prefix string) []v1alpha1.SourceFile {
		var paths []string
		for path := range byPath {
			if strings.HasPrefix(path, prefix) {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		out := make([]v1alpha1.SourceFile, 0, len(paths))
		for _, path := range paths {
			used[path] = true
			out = append(out, v1alpha1.SourceFile{Path: path, Content: byPath[path]})
		}
		return out
	}

	factoryName := strings.TrimSpace(tree.Factory.Name)
	if !validDNSName(factoryName) {
		return nil, fmt.Errorf("factory name %q is not a DNS-1123 subdomain", factoryName)
	}
	ns := opt.Namespace
	var objs []any
	var configMaps []any

	fac, err := factoryObject(tree, take("factory.yaml"), ns)
	if err != nil {
		return nil, err
	}
	objs = append(objs, fac)

	names := sortedKeys(tree.Runners)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("runner name %q is not a DNS-1123 subdomain", name)
		}
		objs = append(objs, runnerObject(factoryName, name, tree.Runners[name], take("runners/"+name+".yaml"), ns))
	}
	names = sortedKeys(tree.Routers)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("router name %q is not a DNS-1123 subdomain", name)
		}
		objs = append(objs, routerObject(factoryName, name, tree.Routers[name], take("routers/"+name+".yaml"), ns))
	}
	names = sortedKeys(tree.Agents)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("agent name %q is not a DNS-1123 subdomain", name)
		}
		agent := tree.Agents[name]
		obj, cm, err := agentObject(factoryName, name, agent, take("agents/"+name+"/agent.md"), ns, opt.InstructionsConfigMap)
		if err != nil {
			return nil, err
		}
		objs = append(objs, obj)
		if cm != nil {
			configMaps = append(configMaps, cm)
		}
	}
	for _, agentName := range sortedKeys(tree.AgentSkills) {
		skills := append([]definition.Skill{}, tree.AgentSkills[agentName]...)
		sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
		for _, skill := range skills {
			objName := agentName + "-" + skill.Name
			if !validDNSName(objName) {
				return nil, fmt.Errorf("skill name %q is not a DNS-1123 subdomain", objName)
			}
			path := "agents/" + agentName + "/skills/" + skill.Name + "/SKILL.md"
			obj, cm := skillObject(factoryName, objName, &v1alpha1.LocalRef{Name: agentName}, skill, take(path), ns, opt.InstructionsConfigMap)
			objs = append(objs, obj)
			if cm != nil {
				configMaps = append(configMaps, cm)
			}
		}
	}
	names = sortedKeys(tree.Automations)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("automation name %q is not a DNS-1123 subdomain", name)
		}
		obj, cm, err := automationObject(factoryName, name, tree.Automations[name], take("automations/"+name+"/automation.md"), ns, opt.InstructionsConfigMap)
		if err != nil {
			return nil, err
		}
		objs = append(objs, obj)
		if cm != nil {
			configMaps = append(configMaps, cm)
		}
	}
	names = sortedKeys(tree.Scorers)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("scorer name %q is not a DNS-1123 subdomain", name)
		}
		obj, cm := scorerObject(factoryName, name, tree.Scorers[name], take("scorers/"+name+"/scorer.md"), ns, opt.InstructionsConfigMap)
		objs = append(objs, obj)
		if cm != nil {
			configMaps = append(configMaps, cm)
		}
	}
	names = sortedKeys(tree.Skills)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("skill name %q is not a DNS-1123 subdomain", name)
		}
		obj, cm := skillObject(factoryName, name, nil, tree.Skills[name], take("skills/"+name+"/SKILL.md"), ns, opt.InstructionsConfigMap)
		objs = append(objs, obj)
		if cm != nil {
			configMaps = append(configMaps, cm)
		}
	}
	names = sortedKeys(tree.Webhooks)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("webhook name %q is not a DNS-1123 subdomain", name)
		}
		objs = append(objs, webhookObject(factoryName, name, tree.Webhooks[name], take("webhooks/"+name+".yaml"), ns))
	}
	names = sortedKeys(tree.Benchmarks)
	for _, name := range names {
		if !validDNSName(name) {
			return nil, fmt.Errorf("benchmark suite name %q is not a DNS-1123 subdomain", name)
		}
		objs = append(objs, benchmarkObject(factoryName, name, tree.Benchmarks[name], takePrefix("benchmarks/"+name+"/"), ns))
	}
	for path := range byPath {
		if !used[path] {
			return nil, fmt.Errorf("definition file %s was not emitted", path)
		}
	}
	objs = append(objs, configMaps...)
	return objs, nil
}

func factoryObject(tree *definition.Tree, files []v1alpha1.SourceFile, ns string) (*v1alpha1.Factory, error) {
	f := tree.Factory
	mcp, err := mcpSpecs(f.MCPServers)
	if err != nil {
		return nil, err
	}
	defaults, err := agentDefaults(f.AgentDefaults)
	if err != nil {
		return nil, err
	}
	inference, err := inferenceSpec(f.Inference)
	if err != nil {
		return nil, err
	}
	scorerDefaults, err := scorerDefaults(f.ScorerDefaults)
	if err != nil {
		return nil, err
	}
	return &v1alpha1.Factory{
		TypeMeta:   typeMeta("Factory"),
		ObjectMeta: objectMeta(f.Name, ns),
		Spec: v1alpha1.FactorySpec{
			SchemaVersion:      f.SchemaVersion,
			Description:        redactText(f.Description),
			Alias:              f.Alias,
			CodeForges:         append([]string{}, f.CodeForges...),
			Repositories:       repos(f.Repositories),
			CredentialStrategy: f.CredentialStrategy,
			Secrets:            namesOnly(f.Secrets),
			SecretRefs:         secretRefs(f.Secrets),
			MCPServers:         mcp,
			AgentDefaults:      defaults,
			Inference:          inference,
			Pipeline:           pipelineSpec(f.Pipeline),
			SelfImprovement:    selfImprovementSpec(f.SelfImprovement),
			ScorerDefaults:     scorerDefaults,
			Files:              files,
		},
	}, nil
}

func runnerObject(factoryName, name string, runner definition.Runner, files []v1alpha1.SourceFile, ns string) *v1alpha1.Runner {
	spec := v1alpha1.RunnerSpec{
		FactoryRef:                     ref(factoryName),
		Description:                    redactText(runner.Description),
		SetupCommands:                  append([]string{}, runner.SetupCommands...),
		FailureSessionRetentionMinutes: int32(runner.FailureSessionRetentionMinutes),
		Files:                          files,
	}
	if runner.VCPUs != 0 || runner.MemoryGB != 0 {
		spec.InstanceShape = &v1alpha1.InstanceShapeSpec{VCPUs: int32(runner.VCPUs), MemoryGB: int32(runner.MemoryGB)}
	}
	if runner.OS != "" || runner.Arch != "" || runner.DockerImage != "" || runner.RegistryCredentialSecretName != "" {
		spec.Platform = &v1alpha1.PlatformSpec{OS: runner.OS, Arch: runner.Arch}
		if runner.DockerImage != "" || runner.RegistryCredentialSecretName != "" {
			spec.Platform.Linux = &v1alpha1.LinuxPlatformSpec{
				DockerImage:                  runner.DockerImage,
				RegistryCredentialSecretName: redactName(runner.RegistryCredentialSecretName),
			}
		}
	}
	return &v1alpha1.Runner{TypeMeta: typeMeta("Runner"), ObjectMeta: objectMeta(name, ns), Spec: spec}
}

func routerObject(factoryName, name string, router definition.Router, files []v1alpha1.SourceFile, ns string) *v1alpha1.ModelRouter {
	spec := v1alpha1.ModelRouterSpec{
		FactoryRef:  ref(factoryName),
		DisplayName: router.DisplayName,
		Type:        router.Type,
		Default:     router.Default,
		Files:       files,
	}
	for _, route := range router.Routes {
		spec.Routes = append(spec.Routes, v1alpha1.RouterRouteSpec{Description: route.Description, Model: route.Model})
	}
	if len(router.Complexity) > 0 {
		spec.Complexity = mapsClone(router.Complexity)
	}
	return &v1alpha1.ModelRouter{TypeMeta: typeMeta("ModelRouter"), ObjectMeta: objectMeta(name, ns), Spec: spec}
}

func agentObject(factoryName, name string, agent definition.Agent, files []v1alpha1.SourceFile, ns string, configMap bool) (*v1alpha1.FactoryAgent, *corev1.ConfigMap, error) {
	if strings.TrimSpace(agent.Model) == "" && agent.Harness == nil {
		return nil, nil, fmt.Errorf("agent %s sets neither model nor harness", name)
	}
	if strings.TrimSpace(agent.Model) != "" && agent.Harness != nil {
		return nil, nil, fmt.Errorf("agent %s sets both model and harness", name)
	}
	mcp, err := mcpSpecs(agent.MCPServers)
	if err != nil {
		return nil, nil, err
	}
	spec := v1alpha1.FactoryAgentSpec{
		FactoryRef:         ref(factoryName),
		AgentType:          agent.AgentType,
		Description:        redactText(agent.Description),
		Model:              agent.Model,
		Harness:            harnessSpec(agent.Harness),
		SpawnableBy:        append([]string{}, agent.SpawnableBy...),
		IdleTimeoutMinutes: int32(agent.IdleTimeoutMinutes),
		Runner:             agent.Runner,
		Pool:               agent.Pool,
		ComputerUseModel:   agent.ComputerUseModel,
		CredentialStrategy: agent.CredentialStrategy,
		MaxTurns:           int32(agent.MaxTurns),
		WorkerHost:         agent.WorkerHost,
		RequiredRepos:      repos(agent.RequiredRepos),
		Secrets:            namesOnly(agent.Secrets),
		SecretRefs:         secretRefs(agent.Secrets),
		MCPServers:         mcp,
		Files:              files,
	}
	if agent.Pool != "" {
		spec.PoolRef = ref(agent.Pool)
	}
	body := redactText(agent.Body)
	var cm *corev1.ConfigMap
	if configMap {
		cm = instructionConfigMap(name, ns, "agent.md", body)
		spec.InstructionsFrom = &v1alpha1.ConfigMapSource{ConfigMapKeyRef: v1alpha1.ConfigMapKeyRef{Name: cm.Name, Key: "agent.md"}}
	} else {
		spec.Instructions = body
	}
	return &v1alpha1.FactoryAgent{TypeMeta: typeMeta("FactoryAgent"), ObjectMeta: objectMeta(name, ns), Spec: spec}, cm, nil
}

func automationObject(factoryName, name string, auto definition.Automation, files []v1alpha1.SourceFile, ns string, configMap bool) (*v1alpha1.Automation, *corev1.ConfigMap, error) {
	triggers, err := triggers(auto.Triggers)
	if err != nil {
		return nil, nil, err
	}
	mcp, err := mcpSpecs(auto.MCPServers)
	if err != nil {
		return nil, nil, err
	}
	spec := v1alpha1.AutomationSpec{
		FactoryRef:    ref(factoryName),
		Enabled:       auto.Enabled,
		AgentRef:      ref(auto.Agent),
		DisplayName:   auto.DisplayName,
		Triggers:      triggers,
		Model:         auto.Model,
		Harness:       harnessSpec(auto.Harness),
		Runner:        auto.Runner,
		Pool:          auto.Pool,
		RequiredRepos: repos(auto.RequiredRepos),
		Secrets:       namesOnly(auto.Secrets),
		SecretRefs:    secretRefs(auto.Secrets),
		MCPServers:    mcp,
		Files:         files,
	}
	body := redactText(auto.Body)
	var cm *corev1.ConfigMap
	if configMap {
		cm = instructionConfigMap(name, ns, "automation.md", body)
		spec.PromptFrom = &v1alpha1.ConfigMapSource{ConfigMapKeyRef: v1alpha1.ConfigMapKeyRef{Name: cm.Name, Key: "automation.md"}}
	} else {
		spec.Prompt = body
	}
	return &v1alpha1.Automation{TypeMeta: typeMeta("Automation"), ObjectMeta: objectMeta(name, ns), Spec: spec}, cm, nil
}

func scorerObject(factoryName, name string, scorer definition.Scorer, files []v1alpha1.SourceFile, ns string, configMap bool) (*v1alpha1.Scorer, *corev1.ConfigMap) {
	spec := v1alpha1.ScorerSpec{
		FactoryRef:      ref(factoryName),
		Description:     redactText(scorer.Description),
		Output:          scorer.Output,
		PassingScore:    scorer.PassingScore,
		SamplingRate:    int32(scorer.SamplingRate),
		Model:           scorer.Model,
		SelfImprovement: scorer.SelfImprovement,
		Files:           files,
	}
	for _, agent := range scorer.Agents {
		spec.Agents = append(spec.Agents, v1alpha1.ScorerAgentSpec{Name: agent.Name, IncludeDescendants: agent.IncludeDescendants})
	}
	for _, label := range scorer.Labels {
		spec.Labels = append(spec.Labels, v1alpha1.ScorerLabelSpec{
			Value:       label.Value,
			Description: redactText(label.Description),
			Score:       label.Score,
		})
	}
	body := redactText(scorer.Body)
	var cm *corev1.ConfigMap
	if configMap {
		cm = instructionConfigMap(name, ns, "scorer.md", body)
		spec.RubricFrom = &v1alpha1.ConfigMapSource{ConfigMapKeyRef: v1alpha1.ConfigMapKeyRef{Name: cm.Name, Key: "scorer.md"}}
	} else {
		spec.Rubric = body
	}
	return &v1alpha1.Scorer{TypeMeta: typeMeta("Scorer"), ObjectMeta: objectMeta(name, ns), Spec: spec}, cm
}

func skillObject(factoryName, name string, agent *v1alpha1.LocalRef, skill definition.Skill, files []v1alpha1.SourceFile, ns string, configMap bool) (*v1alpha1.Skill, *corev1.ConfigMap) {
	spec := v1alpha1.SkillSpec{
		FactoryRef:  ref(factoryName),
		AgentRef:    agent,
		Description: redactText(skill.Description),
		Files:       files,
	}
	body := redactText(skill.Body)
	var cm *corev1.ConfigMap
	if configMap {
		cm = instructionConfigMap(name, ns, "SKILL.md", body)
		spec.InstructionsFrom = &v1alpha1.ConfigMapSource{ConfigMapKeyRef: v1alpha1.ConfigMapKeyRef{Name: cm.Name, Key: "SKILL.md"}}
	} else {
		spec.Instructions = body
	}
	return &v1alpha1.Skill{TypeMeta: typeMeta("Skill"), ObjectMeta: objectMeta(name, ns), Spec: spec}, cm
}

func webhookObject(factoryName, name string, hook definition.Webhook, files []v1alpha1.SourceFile, ns string) *v1alpha1.FactoryWebhook {
	return &v1alpha1.FactoryWebhook{
		TypeMeta:   typeMeta("FactoryWebhook"),
		ObjectMeta: objectMeta(name, ns),
		Spec: v1alpha1.FactoryWebhookSpec{
			FactoryRef:       ref(factoryName),
			AuthMode:         hook.AuthMode,
			SignatureScheme:  hook.SignatureScheme,
			SecretName:       redactName(hook.SecretName),
			DeliveryIDHeader: hook.DeliveryIDHeader,
			Enabled:          hook.Enabled,
			Files:            files,
		},
	}
}

func benchmarkObject(factoryName, name string, suite definition.BenchmarkSuite, files []v1alpha1.SourceFile, ns string) *v1alpha1.BenchmarkSuite {
	spec := v1alpha1.BenchmarkSuiteSpec{
		FactoryRef:  ref(factoryName),
		Description: redactText(suite.Description),
		Agent:       suite.Agent,
		Tasks:       append([]string{}, suite.Tasks...),
		Files:       files,
	}
	for _, cfg := range suite.Configurations {
		item := v1alpha1.BenchmarkConfigurationSpec{Name: cfg.Name, DefinitionRef: cfg.DefinitionRef}
		for _, binding := range cfg.Agents {
			item.Agents = append(item.Agents, v1alpha1.BenchmarkBindingSpec{
				Agent:   binding.Agent,
				Model:   binding.Model,
				Harness: harnessSpec(binding.Harness),
			})
		}
		spec.Configurations = append(spec.Configurations, item)
	}
	return &v1alpha1.BenchmarkSuite{TypeMeta: typeMeta("BenchmarkSuite"), ObjectMeta: objectMeta(name, ns), Spec: spec}
}

func instructionConfigMap(name, namespace, key, body string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(name+"-instructions", namespace),
		Data:       map[string]string{key: body},
	}
}

func typeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: kind}
}

func objectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace}
}

func ref(name string) *v1alpha1.LocalRef {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return &v1alpha1.LocalRef{Name: name}
}

func repos(in []definition.Repository) []v1alpha1.RepositoryRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]v1alpha1.RepositoryRef, len(in))
	for i, repo := range in {
		out[i] = v1alpha1.RepositoryRef{CodeForge: repo.CodeForge, Owner: repo.Owner, Name: repo.Name, Deferred: repo.Deferred}
	}
	return out
}

func namesOnly(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = redactName(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func secretRefs(in []string) []v1alpha1.SecretRef {
	names := namesOnly(in)
	if len(names) == 0 {
		return nil
	}
	out := make([]v1alpha1.SecretRef, len(names))
	for i, name := range names {
		out[i] = v1alpha1.SecretRef{Name: name}
	}
	return out
}

func redactName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if redactText(name) != name {
		return "REDACTED"
	}
	return name
}

func harnessSpec(h *definition.Harness) *v1alpha1.HarnessSpec {
	if h == nil {
		return nil
	}
	out := &v1alpha1.HarnessSpec{Type: h.Type, Model: h.Model}
	if h.Auth != nil {
		out.Auth = &v1alpha1.HarnessAuthSpec{Source: h.Auth.Source, SecretName: redactName(h.Auth.SecretName)}
	}
	return out
}

func agentDefaults(d definition.AgentDefaults) (*v1alpha1.AgentDefaultsSpec, error) {
	mcp, err := mcpSpecs(d.MCPServers)
	if err != nil {
		return nil, err
	}
	spec := &v1alpha1.AgentDefaultsSpec{
		Model:              d.Model,
		Harness:            harnessSpec(d.Harness),
		Runner:             d.Runner,
		Pool:               d.Pool,
		ComputerUseModel:   d.ComputerUseModel,
		Secrets:            namesOnly(d.Secrets),
		MCPServers:         mcp,
		WorkerHost:         d.WorkerHost,
		CredentialStrategy: d.CredentialStrategy,
		IdleTimeoutMinutes: int32(d.IdleTimeoutMinutes),
		MaxTurns:           int32(d.MaxTurns),
	}
	if d.Pool != "" {
		spec.PoolRef = ref(d.Pool)
	}
	if spec.Model == "" && spec.Harness == nil && spec.Runner == "" && spec.Pool == "" && spec.ComputerUseModel == "" &&
		len(spec.Secrets) == 0 && len(spec.MCPServers) == 0 && spec.WorkerHost == "" && spec.CredentialStrategy == "" &&
		spec.IdleTimeoutMinutes == 0 && spec.MaxTurns == 0 {
		return nil, nil
	}
	return spec, nil
}

func inferenceSpec(in *definition.Inference) (*v1alpha1.InferenceSpec, error) {
	if in == nil {
		return nil, nil
	}
	spec := &v1alpha1.InferenceSpec{
		Default: v1alpha1.InferenceDefaultSpec{
			Provider:      in.Default.Provider,
			CredentialRef: redactName(in.Default.CredentialRef),
		},
	}
	if len(in.Providers) > 0 {
		spec.Providers = map[string]v1alpha1.InferenceProviderSpec{}
		for _, name := range sortedKeys(in.Providers) {
			p := in.Providers[name]
			spec.Providers[name] = v1alpha1.InferenceProviderSpec{
				Kind:          p.Kind,
				CredentialRef: redactName(p.CredentialRef),
				Region:        p.Region,
				RoleArn:       p.RoleArn,
				BaseURL:       p.BaseURL,
			}
		}
	}
	return spec, nil
}

func scorerDefaults(in *definition.ScorerDefaults) (*v1alpha1.ScorerDefaultsSpec, error) {
	if in == nil {
		return nil, nil
	}
	mcp, err := mcpSpecs(in.MCPServers)
	if err != nil {
		return nil, err
	}
	return &v1alpha1.ScorerDefaultsSpec{
		Runner:     in.Runner,
		Model:      in.Model,
		Secrets:    namesOnly(in.Secrets),
		MCPServers: mcp,
	}, nil
}

func pipelineSpec(p *definition.Pipeline) *v1alpha1.PipelineSpec {
	if p == nil {
		return nil
	}
	spec := &v1alpha1.PipelineSpec{
		Stages:        append([]string{}, p.Stages...),
		Agents:        mapsClone(p.Agents),
		Skip:          mapsClone(p.Skip),
		RevisionLimit: int32(p.RevisionLimit),
	}
	if len(p.Approvals) > 0 {
		spec.Approvals = map[string]v1alpha1.ApprovalSpec{}
		for name, gate := range p.Approvals {
			spec.Approvals[name] = v1alpha1.ApprovalSpec{Required: gate.Required, Approvers: gate.Approvers}
		}
	}
	return spec
}

func selfImprovementSpec(in *definition.SelfImprovement) *v1alpha1.SelfImprovementSpec {
	if in == nil {
		return nil
	}
	return &v1alpha1.SelfImprovementSpec{
		FailedRunThreshold: int32(in.FailedRunThreshold),
		StaleFailureDays:   int32(in.StaleFailureDays),
		ReviewerType:       in.ReviewerType,
		ReviewerEmails:     append([]string{}, in.ReviewerEmails...),
	}
}

func mcpSpecs(in map[string]definition.MCPServer) (map[string]v1alpha1.MCPServerSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := map[string]v1alpha1.MCPServerSpec{}
	for _, name := range sortedKeys(in) {
		srv := in[name]
		headers, err := stringMapJSON(srv.Headers)
		if err != nil {
			return nil, err
		}
		env, err := stringMapJSON(srv.Env)
		if err != nil {
			return nil, err
		}
		out[name] = v1alpha1.MCPServerSpec{
			URL:     srv.URL,
			Command: srv.Command,
			Args:    append([]string{}, srv.Args...),
			Headers: headers,
			Env:     env,
		}
	}
	return out, nil
}

func stringMapJSON(in map[string]string) (map[string]apiextensionsv1.JSON, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]apiextensionsv1.JSON, len(in))
	for key, value := range in {
		raw, err := json.Marshal(redactText(value))
		if err != nil {
			return nil, err
		}
		out[key] = apiextensionsv1.JSON{Raw: raw}
	}
	return out, nil
}

func triggers(in []definition.Trigger) ([]v1alpha1.TriggerSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]v1alpha1.TriggerSpec, len(in))
	for i, trigger := range in {
		spec := v1alpha1.TriggerSpec{Provider: trigger.Provider, Event: trigger.Event}
		if trigger.Schedule != nil {
			spec.Schedule = &v1alpha1.ScheduleSpec{Name: trigger.Schedule.Name, Cron: trigger.Schedule.Cron}
		}
		if len(trigger.Filter) > 0 {
			raw, err := json.Marshal(redactAny(trigger.Filter))
			if err != nil {
				return nil, err
			}
			spec.Filter = &apiextensionsv1.JSON{Raw: raw}
		}
		out[i] = spec
	}
	return out, nil
}

func mapsClone(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var dns1123Subdomain = func() func(string) bool {
	// DNS-1123 subdomain: lowercase DNS labels separated by dots, max 253.
	return func(name string) bool {
		if name == "" || len(name) > 253 {
			return false
		}
		for _, label := range strings.Split(name, ".") {
			if !dnsLabel(label) {
				return false
			}
		}
		return true
	}
}()

func dnsLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(label)-1:
		default:
			return false
		}
	}
	return true
}

func validDNSName(name string) bool {
	return dns1123Subdomain(name)
}
