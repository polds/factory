package server

import (
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
)

func TestSkillsForMergesFactoryAndAgent(t *testing.T) {
	secret := "planted-secret-value"
	srv := &Server{cfg: Config{Secrets: map[string]string{"M6_PLANTED": secret}}}
	factoryOnly := &definition.Tree{
		Skills: map[string]definition.Skill{
			"cite-sources": {Name: "cite-sources", Body: "factory body"},
		},
	}
	spec, text := srv.skillsFor(factoryOnly, "implementer")
	if spec != "cite-sources" || !strings.Contains(text, "## skill cite-sources") || !strings.Contains(text, "factory body") {
		t.Fatalf("factory skill spec %q text %q", spec, text)
	}

	agentOnly := &definition.Tree{
		AgentSkills: map[string][]definition.Skill{
			"implementer": {{Name: "review", Body: "agent body"}},
		},
	}
	spec, text = srv.skillsFor(agentOnly, "implementer")
	if spec != "review" || !strings.Contains(text, "agent body") {
		t.Fatalf("agent skill spec %q text %q", spec, text)
	}

	clash := &definition.Tree{
		Skills: map[string]definition.Skill{
			"cite-sources": {Name: "cite-sources", Body: "from-factory"},
		},
		AgentSkills: map[string][]definition.Skill{
			"implementer": {{Name: "cite-sources", Body: "from-agent"}},
		},
	}
	spec, text = srv.skillsFor(clash, "implementer")
	if spec != "cite-sources" || !strings.Contains(text, "from-agent") || strings.Contains(text, "from-factory") {
		t.Fatalf("clash spec %q text %q", spec, text)
	}

	both := &definition.Tree{
		Skills: map[string]definition.Skill{
			"zeta": {Name: "zeta", Body: "zeta body " + secret},
		},
		AgentSkills: map[string][]definition.Skill{
			"implementer": {{Name: "alpha", Body: "alpha body"}},
		},
	}
	spec, text = srv.skillsFor(both, "implementer")
	if spec != "alpha,zeta" {
		t.Fatalf("spec %q", spec)
	}
	alphaAt := strings.Index(text, "## skill alpha")
	zetaAt := strings.Index(text, "## skill zeta")
	if alphaAt < 0 || zetaAt < 0 || alphaAt > zetaAt {
		t.Fatalf("text order %q", text)
	}
	if strings.Contains(text, secret) || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("secret leaked %q", text)
	}

	if spec, text = srv.skillsFor(nil, "implementer"); spec != "" || text != "" {
		t.Fatalf("nil tree %q %q", spec, text)
	}
	if spec, text = srv.skillsFor(factoryOnly, ""); spec != "" || text != "" {
		t.Fatalf("empty agent %q %q", spec, text)
	}
	unnamed := &definition.Tree{Skills: map[string]definition.Skill{"": {Name: "", Body: "skip"}}}
	if spec, text = srv.skillsFor(unnamed, "implementer"); spec != "" || text != "" {
		t.Fatalf("unnamed %q %q", spec, text)
	}
}
