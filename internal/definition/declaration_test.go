package definition

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/invocation"
)

func TestDeclarationFixtureValidates(t *testing.T) {
	tree, diags := LoadDir("testdata/valid/declaration")
	if msg := formatDiags(diags); msg != "" {
		t.Fatalf("valid declaration:\n%s", msg)
	}
	if tree == nil || tree.Factory.Name != "payments" {
		t.Fatalf("factory name")
	}
	f := tree.Factory
	if f.RoleSource == nil || f.RoleSource.Seats.PathTemplate != "team/roles/{slug}/{file}" {
		t.Fatalf("roleSource %+v", f.RoleSource)
	}
	if f.RoleSource.Repo.Owner != "acme" || f.RoleSource.Repo.Name != "payments-service" {
		t.Fatalf("role repo %+v", f.RoleSource.Repo)
	}
	if len(f.Repositories) != 2 || f.Repositories[1].CeilingCents != 800 {
		t.Fatalf("repositories %+v", f.Repositories)
	}
	if f.Native == nil || f.Native.Limits.MaxDepth != 8 || f.Native.Limits.AbsoluteMaxDepth != 8 {
		t.Fatalf("native %+v", f.Native)
	}
	if f.Native.Root != "lead" || f.Native.Implementer != "worker" || f.Native.Reviewer != "reviewer" {
		t.Fatalf("seats %+v", f.Native)
	}
	if f.Publish == nil || !f.Publish.Enabled || f.Publish.IssueKey != "payments#120" || f.Publish.RefPrefix != "factory/draft-" {
		t.Fatalf("publish %+v", f.Publish)
	}
	if f.Publish.Repo == nil || f.Publish.Repo.Name != "payments-service" {
		t.Fatalf("publish repo %+v", f.Publish)
	}
	if f.Evidence == nil || f.Evidence.Package != "./oracle" || f.Evidence.Mutation != "oracle/oracle.go" {
		t.Fatalf("evidence %+v", f.Evidence)
	}
	if f.Evidence.Sentinel != "factoryEvidencePass" || f.Evidence.Break != "factoryEvidenceFail" {
		t.Fatalf("markers %+v", f.Evidence)
	}
	if f.Budget == nil || f.Budget.CeilingCents != 2500 {
		t.Fatalf("budget %+v", f.Budget)
	}
	if f.Inference == nil || f.Inference.Rates == nil || f.Inference.Rates.PromptByteCap != 400000 || f.Inference.Rates.MaxTokens != 8192 {
		t.Fatalf("rates %+v", f.Inference)
	}
	if len(f.Inference.Rates.Models) != 1 || f.Inference.Rates.Models[0].Name != "example-model" {
		t.Fatalf("models %+v", f.Inference.Rates)
	}
}

func TestDeclarationDefaultsAndOmission(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "factory.yaml"), `schemaVersion: v1alpha1
name: t
native:
  implementer: worker
  reviewer: reviewer
evidence: {}
`)
	tree, diags := LoadDir(dir)
	if msg := formatDiags(diags); msg != "" {
		t.Fatalf("defaults:\n%s", msg)
	}
	if tree.Factory.Native.Limits.MaxDepth != DefaultNativeMaxDepth || tree.Factory.Native.Limits.AbsoluteMaxDepth != DefaultNativeAbsoluteMaxDepth {
		t.Fatalf("limits %+v", tree.Factory.Native.Limits)
	}
	if tree.Factory.Evidence.Sentinel != DefaultEvidenceSentinel || tree.Factory.Evidence.Break != DefaultEvidenceBreak {
		t.Fatalf("markers %+v", tree.Factory.Evidence)
	}
	if tree.Factory.RoleSource != nil || tree.Factory.Publish != nil || tree.Factory.Budget != nil {
		t.Fatalf("omitted objects were injected: %+v", tree.Factory)
	}

	omitted, diags := LoadDir("testdata/valid/m2")
	if msg := formatDiags(diags); msg != "" {
		t.Fatalf("legacy:\n%s", msg)
	}
	if omitted.Factory.RoleSource != nil || omitted.Factory.Native != nil || omitted.Factory.Publish != nil || omitted.Factory.Evidence != nil || omitted.Factory.Budget != nil {
		t.Fatalf("legacy objects %+v", omitted.Factory)
	}
	if omitted.Factory.Inference != nil && omitted.Factory.Inference.Rates != nil {
		t.Fatal("legacy rates")
	}
}

func TestLegacyDigestsUnchanged(t *testing.T) {
	cases := []struct {
		path   string
		digest string
	}{
		{"testdata/valid/m2", "38ec9ec705ddcc08230f5dde5d7ef4c96aaef9e3a2f735d71d33409ec386162f"},
		{"testdata/valid/acme-factory", "7a5b3298f8474ad5c4fcbb6cc8bbaced47d2ef98f343fdef39f121415860db8c"},
		{"../../examples/acme-factory", "0fddde0bd1c32b37af127fd93dc88ac6cb36726914755805b355e0a46d878361"},
	}
	for _, tc := range cases {
		tree, diags := LoadDir(tc.path)
		if msg := errorText(diags); msg != "" {
			t.Fatalf("%s:\n%s", tc.path, msg)
		}
		if tree.Digest != tc.digest {
			t.Fatalf("%s digest %s", tc.path, tree.Digest)
		}
	}
	checked, err := ValidateNativeTree(proofTree())
	const witnessDigest = "14faa7694f746a143133d339b3243aafef69e9bda270000fee48b737324a1c82"
	if err != nil || checked.Digest != witnessDigest {
		t.Fatalf("witness %s %v", checked.Digest, err)
	}
	const definedFactoryDigest = "b67e84668f3df343d6203b54a72377e2eb59ca7d919a9463efeab47c7b7111a0"
	req := invocation.Request{
		DefinitionRevision:  "rev-applied-1",
		Profile:             invocation.Profile{Name: "scan"},
		Target:              &invocation.Target{Repository: "example/atum-core", PullRequest: 2213},
		Inputs:              map[string]any{"instruction": "review"},
		SourceOccurrenceKey: "human:review:1",
		Correlation:         &invocation.Correlation{ThreadRef: "slack:T/C/1555"},
		Intent:              "new",
		Priority:            "interactive",
	}
	if got := invocation.Digest(req); got != definedFactoryDigest {
		t.Fatalf("defined-factory digest %s", got)
	}
}

func TestDeclarationRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "absoluteMaxDepth",
			yaml: `schemaVersion: v1alpha1
name: t
native:
  limits:
    absoluteMaxDepth: 9
`,
			want: "absoluteMaxDepth must be from 1 to 8",
		},
		{
			name: "publish repo",
			yaml: `schemaVersion: v1alpha1
name: t
repositories:
  - { codeForge: GITHUB, owner: acme, name: payments-service }
publish:
  repo: { codeForge: GITHUB, owner: acme, name: other-service }
`,
			want: "publish.repo is not in repositories",
		},
		{
			name: "publish ceiling",
			yaml: `schemaVersion: v1alpha1
name: t
repositories:
  - { codeForge: GITHUB, owner: acme, name: payments-service }
publish:
  enabled: true
  repo: { codeForge: GITHUB, owner: acme, name: payments-service }
evidence:
  package: ./oracle
  mutation: oracle/oracle.go
native:
  implementer: worker
  reviewer: reviewer
  seats: [worker, reviewer]
`,
			want: "budget.ceilingCents is required when publish.enabled is true",
		},
		{
			name: "path template",
			yaml: `schemaVersion: v1alpha1
name: t
repositories:
  - { codeForge: GITHUB, owner: acme, name: payments-service }
roleSource:
  repo: { codeForge: GITHUB, owner: acme, name: payments-service }
  roster: { path: team/roster.yaml }
  seats:
    pathTemplate: "team/roles/{file}"
    files: [ROLE.md]
budget:
  ceilingCents: 100
`,
			want: "seats.pathTemplate must contain {slug} once",
		},
		{
			name: "unknown key",
			yaml: `schemaVersion: v1alpha1
name: t
roleSource:
  extra: true
`,
			want: `unknown key "extra"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "factory.yaml"), tc.yaml)
			_, diags := LoadDir(dir)
			if !errorMentions(diags, tc.want) {
				t.Fatalf("diagnostics:\n%s", formatDiags(diags))
			}
		})
	}
}

func TestValidateNativeTreeBehaviorUnchanged(t *testing.T) {
	checked, err := ValidateNativeTree(proofTree())
	const witnessDigest = "14faa7694f746a143133d339b3243aafef69e9bda270000fee48b737324a1c82"
	if err != nil || checked.Digest != witnessDigest || checked.Depth["specialist-a"] != 2 {
		t.Fatalf("proof %+v %v", checked, err)
	}
	wider := proofTree()
	wider.MaxDepth = 8
	if _, err := ValidateNativeTree(wider); err == nil {
		t.Fatal("maxDepth 8 accepted")
	}
	deeper := proofTree()
	deeper.AbsoluteMaxDepth = 9
	if _, err := ValidateNativeTree(deeper); err == nil {
		t.Fatal("absoluteMaxDepth 9 accepted")
	}
	plain := proofTree()
	plain.Synthetic = false
	if _, err := ValidateNativeTree(plain); err == nil {
		t.Fatal("non-synthetic accepted")
	}
}

func errorText(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		if d.Severity != SeverityError {
			continue
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", d.File, d.Line, d.Message)
	}
	return b.String()
}

func errorMentions(ds []Diagnostic, sub string) bool {
	for _, d := range ds {
		if d.Severity == SeverityError && strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}
