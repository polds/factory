package definition

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func TestInheritSparseOverride(t *testing.T) {
	defaults := AgentDefaults{
		Model:            "auto",
		Runner:           "local",
		Pool:             "std",
		ComputerUseModel: "cu",
		MaxTurns:         40,
		Secrets:          []string{"DEFAULT"},
	}
	got := Inherit(Factory{}, defaults, Agent{Pool: "gpu", Description: "keeps its own"})
	if got.Model != "auto" || got.Harness != nil || got.Runner != "local" || got.Pool != "gpu" {
		t.Fatalf("sparse = %+v", got)
	}
	if got.ComputerUseModel != "cu" || got.MaxTurns != 40 || got.Description != "keeps its own" {
		t.Fatalf("defaults not filled = %+v", got)
	}
	if !reflect.DeepEqual(got.Secrets, []string{"DEFAULT"}) {
		t.Fatalf("omitted secrets = %#v", got.Secrets)
	}
}

func TestInheritReplaceNotMerge(t *testing.T) {
	defaults := AgentDefaults{
		Model:   "auto",
		Runner:  "local",
		Secrets: []string{"DEFAULT"},
		MCPServers: map[string]MCPServer{
			"local": {URL: "http://local"},
		},
	}
	got := Inherit(Factory{}, defaults, Agent{
		Model:   "claude-x",
		Secrets: []string{"OWN"},
		MCPServers: map[string]MCPServer{
			"own": {URL: "http://own"},
		},
	})
	if !reflect.DeepEqual(got.Secrets, []string{"OWN"}) {
		t.Fatalf("secrets merged: %#v", got.Secrets)
	}
	if _, ok := got.MCPServers["local"]; ok {
		t.Fatalf("mcp merged: %#v", got.MCPServers)
	}
	if got.MCPServers["own"].URL != "http://own" {
		t.Fatalf("mcp = %#v", got.MCPServers)
	}

	cleared := Inherit(Factory{}, defaults, Agent{Model: "claude-x", Secrets: []string{}, MCPServers: map[string]MCPServer{}})
	if len(cleared.Secrets) != 0 || len(cleared.MCPServers) != 0 {
		t.Fatalf("empty replacement inherited defaults: %+v", cleared)
	}
}

func TestInheritModelAutoEqualsWarpHarness(t *testing.T) {
	defaults := AgentDefaults{Runner: "local"}
	byModel := Inherit(Factory{}, defaults, Agent{Model: "auto"})
	byWarp := Inherit(Factory{}, defaults, Agent{Harness: &Harness{Type: "warp-agent", Model: "auto"}})
	byOz := Inherit(Factory{}, defaults, Agent{Harness: &Harness{Type: "oz", Model: "auto"}})
	if CanonicalModel(byModel) != "warp-agent/auto" {
		t.Fatalf("model auto = %q", CanonicalModel(byModel))
	}
	if CanonicalModel(byModel) != CanonicalModel(byWarp) || CanonicalModel(byWarp) != CanonicalModel(byOz) {
		t.Fatalf("auto forms differ: %q %q %q", CanonicalModel(byModel), CanonicalModel(byWarp), CanonicalModel(byOz))
	}
	other := Inherit(Factory{}, defaults, Agent{Harness: &Harness{Type: "claude-code", Model: "auto"}})
	if CanonicalModel(other) == CanonicalModel(byModel) {
		t.Fatal("claude-code/auto collapsed into warp-agent/auto")
	}
	if byModel.Model != "auto" || byModel.Harness != nil {
		t.Fatalf("model auto rewritten: %+v", byModel)
	}
}

func TestPropertyFactorySecretsUnion(t *testing.T) {
	for i := 0; i < 40; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0x5EC))
		factorySecrets := randNames(rng, rng.IntN(4))
		defaultSecrets := randNames(rng, rng.IntN(4))
		agentSecrets := randNames(rng, rng.IntN(4))
		setAgent := rng.IntN(2) == 0
		declared := Agent{Model: "m"}
		if setAgent {
			declared.Secrets = append([]string{}, agentSecrets...)
		}
		factory := Factory{Secrets: factorySecrets}
		defaults := AgentDefaults{Model: "m", Runner: "r", Secrets: defaultSecrets}
		resolved := Inherit(factory, defaults, declared)
		got := EffectiveSecrets(factory, resolved)

		var want []string
		if setAgent {
			want = expectedUnion(factorySecrets, agentSecrets)
			for _, name := range defaultSecrets {
				if containsName(factorySecrets, name) || containsName(agentSecrets, name) {
					continue
				}
				if containsName(got, name) {
					t.Fatalf("i=%d defaults secret %s leaked into %#v", i, name, got)
				}
			}
			for _, name := range factorySecrets {
				if containsName(agentSecrets, name) {
					continue
				}
				if containsName(resolved.Secrets, name) {
					t.Fatalf("i=%d factory secret %s copied onto agent %#v", i, name, resolved.Secrets)
				}
			}
		} else {
			want = expectedUnion(factorySecrets, defaultSecrets)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("i=%d effective %#v want %#v set=%v", i, got, want, setAgent)
		}
	}
}

func TestPropertyMCPServersUnion(t *testing.T) {
	for i := 0; i < 40; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0xC0))
		factoryName := randNames(rng, 1)[0]
		defaultName := randNames(rng, 1)[0]
		agentName := randNames(rng, 1)[0]
		factory := Factory{MCPServers: map[string]MCPServer{factoryName: {URL: "http://factory"}}}
		defaults := AgentDefaults{
			Model:      "m",
			Runner:     "r",
			MCPServers: map[string]MCPServer{defaultName: {URL: "http://default"}},
		}
		setAgent := rng.IntN(2) == 0
		declared := Agent{Model: "m"}
		if setAgent {
			declared.MCPServers = map[string]MCPServer{agentName: {URL: "http://agent"}}
		}
		resolved := Inherit(factory, defaults, declared)
		got := EffectiveMCPServers(factory, resolved)
		if got[factoryName].URL != "http://factory" {
			t.Fatalf("i=%d factory server missing: %#v", i, got)
		}
		if setAgent {
			if got[agentName].URL != "http://agent" && agentName != factoryName {
				t.Fatalf("i=%d agent server missing: %#v", i, got)
			}
			if defaultName != factoryName && defaultName != agentName {
				if _, ok := got[defaultName]; ok {
					t.Fatalf("i=%d defaults server merged: %#v", i, got)
				}
			}
			if _, ok := resolved.MCPServers[factoryName]; ok && factoryName != agentName {
				t.Fatalf("i=%d factory server copied onto agent: %#v", i, resolved.MCPServers)
			}
		} else if got[defaultName].URL != "http://default" && defaultName != factoryName {
			t.Fatalf("i=%d inherited default missing: %#v", i, got)
		}
		if factoryName == agentName && got[factoryName].URL != "http://factory" {
			t.Fatalf("i=%d factory server lost the name collision: %#v", i, got)
		}
	}
}

func TestPropertyRequiredReposExtend(t *testing.T) {
	for i := 0; i < 40; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0x0E0))
		agentRepos := randRepos(rng, rng.IntN(4))
		autoRepos := randRepos(rng, rng.IntN(4))
		omit := rng.IntN(2) == 0
		auto := Automation{Agent: "impl", Model: "other"}
		if !omit {
			auto.RequiredRepos = append([]Repository{}, autoRepos...)
		}
		if rng.IntN(2) == 0 {
			auto.Secrets = []string{"AUTO"}
		}
		got := InheritAutomation(Agent{
			Model:         "agent-model",
			Runner:        "local",
			Secrets:       []string{"AGENT"},
			RequiredRepos: agentRepos,
		}, auto)
		want := append([]Repository{}, agentRepos...)
		if !omit {
			want = append(want, autoRepos...)
		}
		if !reflect.DeepEqual(got.RequiredRepos, want) {
			t.Fatalf("i=%d repos %#v want %#v", i, got.RequiredRepos, want)
		}
		if auto.Secrets != nil {
			if !reflect.DeepEqual(got.Secrets, []string{"AUTO"}) {
				t.Fatalf("i=%d secrets replaced incorrectly: %#v", i, got.Secrets)
			}
		} else if !reflect.DeepEqual(got.Secrets, []string{"AGENT"}) {
			t.Fatalf("i=%d secrets not inherited: %#v", i, got.Secrets)
		}
		if got.Model != "other" || got.Runner != "local" {
			t.Fatalf("i=%d model/runner = %+v", i, got)
		}
	}
}

func TestPropertyModelXOR(t *testing.T) {
	layers := []string{"defaults", "agent", "automation"}
	for i := 0; i < 30; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0xA0B))
		layer := layers[rng.IntN(len(layers))]
		setModel := rng.IntN(2) == 0
		setHarness := rng.IntN(2) == 0
		dir := t.TempDir()
		writeLayerTree(t, dir, layer, setModel, setHarness)
		_, diags := LoadDir(dir)
		got := diagHas(diags, "mutually exclusive")
		want := setModel && setHarness
		if got != want {
			t.Fatalf("i=%d layer=%s model=%v harness=%v exclusive=%v\n%s", i, layer, setModel, setHarness, got, formatDiags(diags))
		}
	}
}

func TestPropertySparseOverride(t *testing.T) {
	for i := 0; i < 40; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 0x5A))
		defPool := randNames(rng, 1)[0]
		agentPool := ""
		if rng.IntN(2) == 0 {
			agentPool = randNames(rng, 1)[0]
		}
		defTurns := rng.IntN(5) + 1
		agentTurns := 0
		if rng.IntN(2) == 0 {
			agentTurns = rng.IntN(5) + 1
		}
		declared := Agent{Model: "kept"}
		if agentPool != "" {
			declared.Pool = agentPool
		}
		if agentTurns != 0 {
			declared.MaxTurns = agentTurns
		}
		got := Inherit(Factory{}, AgentDefaults{Model: "def", Runner: "r", Pool: defPool, MaxTurns: defTurns}, declared)
		if got.Model != "kept" {
			t.Fatalf("i=%d model %q", i, got.Model)
		}
		if agentPool != "" && got.Pool != agentPool {
			t.Fatalf("i=%d pool %q", i, got.Pool)
		}
		if agentPool == "" && got.Pool != defPool {
			t.Fatalf("i=%d pool inherited %q", i, got.Pool)
		}
		if agentTurns != 0 && got.MaxTurns != agentTurns {
			t.Fatalf("i=%d turns %d", i, got.MaxTurns)
		}
		if agentTurns == 0 && got.MaxTurns != defTurns {
			t.Fatalf("i=%d turns inherited %d", i, got.MaxTurns)
		}
	}
}

func writeLayerTree(t *testing.T, dir, layer string, setModel, setHarness bool) {
	t.Helper()
	model, harness := yamlModel(layer == "defaults", setModel, setHarness)
	factory := "schemaVersion: v1alpha1\nname: xor\nagentDefaults:\n  runner: local\n"
	if layer == "defaults" {
		factory += model + harness
	} else {
		factory += "  model: auto\n"
	}
	writeFile(t, dir+"/factory.yaml", factory)
	writeFile(t, dir+"/runners/local.yaml", "description: local\nplatform:\n  os: linux\n  linux:\n    dockerImage: img\n")
	agentModel, agentHarness := "model: auto\n", ""
	if layer == "agent" {
		agentModel, agentHarness = yamlModel(false, setModel, setHarness)
	}
	writeFile(t, dir+"/agents/impl/agent.md", "---\ndescription: impl\nagentType: IMPLEMENT\n"+agentModel+agentHarness+"---\n\n# Impl\n")
	if layer == "automation" {
		autoModel, autoHarness := yamlModel(false, setModel, setHarness)
		writeFile(t, dir+"/automations/go/automation.md", "---\nenabled: true\nagent: impl\n"+autoModel+autoHarness+"---\n\n# Go\n")
	}
}

func yamlModel(indented, setModel, setHarness bool) (model, harness string) {
	pad := ""
	if indented {
		pad = "  "
	}
	if setModel {
		model = pad + "model: claude-x\n"
	}
	if setHarness {
		harness = pad + "harness:\n" + pad + "  type: claude-code\n" + pad + "  model: claude-y\n"
	}
	return model, harness
}

func randNames(rng *rand.Rand, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "S"+string(rune('A'+rng.IntN(26)))+string(rune('A'+rng.IntN(26))))
	}
	return out
}

func randRepos(rng *rand.Rand, n int) []Repository {
	out := make([]Repository, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Repository{CodeForge: "GITHUB", Owner: "acme", Name: randNames(rng, 1)[0]})
	}
	return out
}

func expectedUnion(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func containsName(list []string, name string) bool {
	return strings.Contains(","+strings.Join(list, ",")+",", ","+name+",")
}

func diagHas(ds []Diagnostic, sub string) bool {
	for _, d := range ds {
		if strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}
