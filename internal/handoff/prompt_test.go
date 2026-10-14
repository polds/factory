package handoff

import (
	"encoding/base64"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
)

func TestBuildPromptGoldenTwoStage(t *testing.T) {
	t.Parallel()
	got := BuildPrompt(goldenAgent(), goldenContext())
	again := BuildPrompt(goldenAgent(), goldenContext())
	if got != again {
		t.Fatal("BuildPrompt is not deterministic")
	}
	want, err := os.ReadFile("testdata/golden_prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	triage := strings.Index(got, "Scope is the payments docs refund link.")
	planning := strings.Index(got, "Update docs/refunds.md anchor and add a link check.")
	specURI := strings.Index(got, "s3://factory/wi_01HQAND0FF0000000000000000/spec.md")
	planURI := strings.Index(got, "s3://factory/wi_01HQAND0FF0000000000000000/plan.md")
	if triage < 0 || planning < 0 || triage > planning {
		t.Fatalf("prior summaries out of order: triage=%d planning=%d", triage, planning)
	}
	if specURI < 0 || planURI < 0 || specURI > planURI {
		t.Fatalf("artifact URIs out of order: spec=%d plan=%d", specURI, planURI)
	}
	if !strings.Contains(got, "Fix the broken documentation link") || !strings.Contains(got, "Point them at /v2/refunds.") {
		t.Fatal("golden prompt missing title or body")
	}
}

func TestBuildPromptEmptyPriorStages(t *testing.T) {
	t.Parallel()
	agent := definition.Agent{Name: "triage", Body: "Sort the intake."}
	ctx := Context{Title: "Hello", Body: "World"}
	got := BuildPrompt(agent, ctx)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") || !strings.Contains(got, "Sort the intake.") {
		t.Fatalf("empty prior prompt missing work item or instructions:\n%s", got)
	}
	if !strings.Contains(got, "# Prior stage outputs\n(none)\n") {
		t.Fatalf("empty prior stages not marked:\n%s", got)
	}
	ctx.PriorStages = []StageOutput{}
	if BuildPrompt(agent, ctx) != got {
		t.Fatal("nil and empty prior stages differ")
	}
}

func TestBuildPromptOmitsSecretValues(t *testing.T) {
	t.Parallel()
	const (
		planted   = "ghp_Planted/SecretValue99"
		onlyMCP   = "sk-OnlyInMCPEnvironmentValue"
		headerTok = "ghp_HeaderOnlyPlantedToken"
	)
	agent := definition.Agent{
		Name: "implementer",
		Body: "Use {{GH_TOKEN}} from the environment. Never print it.",
		MCPServers: map[string]definition.MCPServer{
			"github": {
				Env: map[string]string{
					"GH_TOKEN":    onlyMCP,
					"REF_ONLY":    "{{GH_TOKEN}}",
					"AUTH_HEADER": "Bearer {{GH_TOKEN}}",
				},
				Headers: map[string]string{
					"Authorization": "Bearer " + headerTok,
				},
			},
		},
	}
	ctx := Context{
		Title: "Ship the fix",
		Body:  "issue text mentions " + planted,
		Env: map[string]string{
			"GH_TOKEN":        planted,
			"OPENAI_API_KEY":  "sk-plantedopenaikeyvalue",
			"TEMPLATE":        "{{GH_TOKEN}}",
			"TEMPLATE_BEARER": "Bearer {{NPM_TOKEN}}",
		},
		PriorStages: []StageOutput{{
			Stage:       "triage",
			Agent:       "triage",
			RunID:       "run_01",
			SummaryText: "saw " + planted + " and " + url.QueryEscape(planted) + " and " + headerTok,
		}},
	}
	got := BuildPrompt(agent, ctx)
	forbidden := []string{
		planted,
		url.QueryEscape(planted),
		onlyMCP,
		headerTok,
		"sk-plantedopenaikeyvalue",
	}
	for _, secret := range forbidden {
		if strings.Contains(got, secret) {
			t.Errorf("prompt contains secret %q\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "issue text mentions [REDACTED]") {
		t.Fatalf("body secret was not redacted:\n%s", got)
	}
	if !strings.Contains(got, "saw [REDACTED] and [REDACTED] and [REDACTED]") {
		t.Fatalf("summary secrets were not redacted:\n%s", got)
	}
	if !strings.Contains(got, "{{GH_TOKEN}}") {
		t.Fatalf("secret reference was stripped:\n%s", got)
	}
	if !strings.Contains(got, "Ship the fix") {
		t.Fatal("title missing")
	}
}

func TestBuildPromptRedactsEncodedSecrets(t *testing.T) {
	t.Parallel()
	t.Run("lowercase hex", func(t *testing.T) {
		t.Parallel()
		const secret = "ghp_Planted/SecretValue99"
		const encoded = "ghp_Planted%2fSecretValue99"
		got := promptWith(map[string]string{"GH_TOKEN": secret}, "mentions "+encoded, nil)
		if strings.Contains(got, encoded) || strings.Contains(got, secret) {
			t.Fatalf("lowercase percent-encoding leaked:\n%s", got)
		}
		if !strings.Contains(got, "mentions [REDACTED]") {
			t.Fatalf("lowercase form was not redacted:\n%s", got)
		}
	})
	t.Run("space as percent20", func(t *testing.T) {
		t.Parallel()
		const secret = "abcd secret/token"
		plus := url.QueryEscape(secret) // abcd+secret%2Ftoken
		path := "abcd%20secret%2Ftoken"
		lower := "abcd%20secret%2ftoken"
		got := promptWith(map[string]string{"GH_TOKEN": secret}, plus+" "+path+" "+lower, nil)
		for _, form := range []string{secret, plus, path, lower} {
			if strings.Contains(got, form) {
				t.Errorf("form %q leaked:\n%s", form, got)
			}
		}
	})
	t.Run("path escape", func(t *testing.T) {
		t.Parallel()
		const secret = "user@host/secret"
		query := url.QueryEscape(secret) // user%40host%2Fsecret
		path := url.PathEscape(secret)   // user@host%2Fsecret
		lower := "user@host%2fsecret"    // lowercase path form
		got := promptWith(map[string]string{"GH_TOKEN": secret}, secret+" "+query+" "+path+" "+lower, nil)
		for _, form := range []string{secret, query, path, lower} {
			if strings.Contains(got, form) {
				t.Errorf("form %q leaked:\n%s", form, got)
			}
		}
	})
	t.Run("short prefix does not punch a hole", func(t *testing.T) {
		t.Parallel()
		const secret = "ghp_Planted/SecretValue99"
		const encoded = "ghp_Planted%2fSecretValue99"
		got := promptWith(map[string]string{
			"PREFIX":   "ghp_",
			"GH_TOKEN": secret,
		}, "mentions "+encoded, nil)
		if strings.Contains(got, encoded) || strings.Contains(got, "Planted%2fSecretValue99") {
			t.Fatalf("short prefix punched a hole:\n%s", got)
		}
		if !strings.Contains(got, "mentions [REDACTED]") {
			t.Fatalf("encoded secret was not consumed:\n%s", got)
		}
	})
}

func TestBuildPromptExtractsBareCredentials(t *testing.T) {
	t.Parallel()
	const bare = "ghp_HeaderOnlyPlantedToken"
	t.Run("token scheme", func(t *testing.T) {
		t.Parallel()
		got := promptWith(nil, "saw "+bare, map[string]string{
			"Authorization": "token " + bare,
		})
		if strings.Contains(got, bare) {
			t.Fatalf("bare token leaked:\n%s", got)
		}
	})
	t.Run("quoted bearer", func(t *testing.T) {
		t.Parallel()
		got := promptWith(nil, "saw "+bare, map[string]string{
			"Authorization": `Bearer "` + bare + `"`,
		})
		if strings.Contains(got, bare) {
			t.Fatalf("quoted bearer leaked:\n%s", got)
		}
	})
	t.Run("trailing punctuation", func(t *testing.T) {
		t.Parallel()
		got := promptWith(nil, "saw "+bare, map[string]string{
			"Authorization": "Bearer " + bare + ",",
		})
		if strings.Contains(got, bare) {
			t.Fatalf("trailing punctuation kept the comma on the needle:\n%s", got)
		}
	})
	t.Run("url userinfo password", func(t *testing.T) {
		t.Parallel()
		const password = "SuperSecretValue99"
		got := promptWith(map[string]string{
			"DATABASE_URL": "postgres://user:" + password + "@db.internal/app",
		}, "password "+password, nil)
		if strings.Contains(got, password) {
			t.Fatalf("userinfo password leaked:\n%s", got)
		}
		if !strings.Contains(got, "password [REDACTED]") {
			t.Fatalf("password was not redacted:\n%s", got)
		}
	})
}

func TestBuildPromptProtectsSecretReferences(t *testing.T) {
	t.Parallel()
	agent := definition.Agent{
		Name: "implementer",
		Body: "Use {{GH_TOKEN}} and {{NPM_TOKEN}}.",
	}
	got := BuildPrompt(agent, Context{
		Title: "T",
		Body:  "TOKEN is not the placeholder {{GH_TOKEN}}.",
		Env:   map[string]string{"LOW": "TOKEN"},
	})
	if !strings.Contains(got, "{{GH_TOKEN}}") || !strings.Contains(got, "{{NPM_TOKEN}}") {
		t.Fatalf("placeholder rewritten:\n%s", got)
	}
	if strings.Contains(got, "TOKEN is not") {
		t.Fatalf("bare TOKEN was not redacted:\n%s", got)
	}
}

func TestBuildPromptShortSecretFloor(t *testing.T) {
	t.Parallel()
	// harness.Redact ignores secrets shorter than 4 bytes. A short value that
	// already appears in included text stays. A short value that appears only
	// in the environment is still omitted.
	const inBody = "q7z"
	const onlyEnv = "xy"
	got := BuildPrompt(definition.Agent{Name: "implementer", Body: "Do the work."}, Context{
		Title: "T",
		Body:  "keep " + inBody + " visible",
		Env:   map[string]string{"A": inBody, "B": onlyEnv},
	})
	if !strings.Contains(got, "keep q7z visible") {
		t.Fatalf("short secret was redacted:\n%s", got)
	}
	if strings.Contains(got, onlyEnv) {
		t.Fatalf("env-only short secret was copied:\n%s", got)
	}
}

func TestBuildPromptDoesNotCopySecretNames(t *testing.T) {
	t.Parallel()
	const name = "NPM_TOKEN_NAME_ONLY"
	got := BuildPrompt(definition.Agent{
		Name:    "implementer",
		Body:    "Do the work.",
		Secrets: []string{name},
	}, Context{Title: "T", Body: "B"})
	if strings.Contains(got, name) {
		t.Fatalf("secret name copied into the prompt:\n%s", got)
	}
}

func TestBuildPromptRedactionEdgeCases(t *testing.T) {
	t.Parallel()
	const basicUser = "alice:SuperSecretValue99"
	basicStd := base64.StdEncoding.EncodeToString([]byte(basicUser))
	basicRaw := base64.RawStdEncoding.EncodeToString([]byte(basicUser))
	tests := []struct {
		name      string
		env       map[string]string
		headers   map[string]string
		body      string
		forbidden []string
		want      string
	}{
		{
			name: "placeholder split across chunks",
			env:  map[string]string{"GH_TOKEN": "ghp_supersecret{{GH_TOKEN}}"},
			body: "val ghp_supersecret{{GH_TOKEN}}",
			forbidden: []string{
				"ghp_supersecret",
				"ghp_supersecret{{GH_TOKEN}}",
			},
			want: "val [REDACTED]",
		},
		{
			name: "mixed-case hex tokens",
			env:  map[string]string{"GH_TOKEN": "a/b/c"},
			body: "tok a%2Fb%2fc end",
			forbidden: []string{
				"a%2Fb%2fc",
				"a/b/c",
			},
			want: "tok [REDACTED] end",
		},
		{
			name: "schemeless userinfo",
			env:  map[string]string{"DATABASE_URL": "user:SuperSecretValue99@db.internal"},
			body: "password SuperSecretValue99 at db.internal",
			forbidden: []string{
				"SuperSecretValue99",
				"user:SuperSecretValue99@db.internal",
			},
			want: "password [REDACTED] at db.internal",
		},
		{
			name: "secrets split by newlines",
			env: map[string]string{
				"MULTI": "ghp_PlantedSecretOne99\r\nsk-PlantedSecretTwo99",
			},
			body: "saw ghp_PlantedSecretOne99 and sk-PlantedSecretTwo99",
			forbidden: []string{
				"ghp_PlantedSecretOne99",
				"sk-PlantedSecretTwo99",
			},
			want: "saw [REDACTED] and [REDACTED]",
		},
		{
			name:    "decoded HTTP Basic credentials",
			headers: map[string]string{"Authorization": "Basic " + basicStd},
			body:    "user alice:SuperSecretValue99 and SuperSecretValue99",
			forbidden: []string{
				"alice:SuperSecretValue99",
				"SuperSecretValue99",
				basicStd,
			},
			want: "user [REDACTED] and [REDACTED]",
		},
		{
			name:    "decoded HTTP Basic credentials without padding",
			headers: map[string]string{"Authorization": "Basic " + basicRaw},
			body:    "user alice:SuperSecretValue99 and SuperSecretValue99",
			forbidden: []string{
				"alice:SuperSecretValue99",
				"SuperSecretValue99",
				basicRaw,
			},
			want: "user [REDACTED] and [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := promptWith(tt.env, tt.body, tt.headers)
			for _, secret := range tt.forbidden {
				if strings.Contains(got, secret) {
					t.Errorf("leaked %q\n%s", secret, got)
				}
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("missing %q\n%s", tt.want, got)
			}
			if strings.Count(got, "{{GH_TOKEN}}") != 1 {
				t.Fatalf("placeholder count = %d\n%s", strings.Count(got, "{{GH_TOKEN}}"), got)
			}
		})
	}
}

func TestRedactPromptMatchesHarnessWithoutPlaceholders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		secrets []string
	}{
		{"mentions ghp_Planted%2fSecretValue99", []string{"ghp_Planted/SecretValue99"}},
		{"abcd+secret%2Ftoken abcd%20secret%2Ftoken", []string{"abcd secret/token"}},
		{
			base64.StdEncoding.EncodeToString([]byte("ghp_Planted/SecretValue99")),
			[]string{"ghp_Planted/SecretValue99"},
		},
		{`xxaaaa\"bbbbxx`, []string{`aaaa"bbbb`}},
	}
	for _, tc := range cases {
		got := redactPrompt(tc.in, tc.secrets)
		want := harness.Redact(tc.in, expandSecrets(tc.secrets))
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	}
}

func TestBuildPromptRedactsBase64Secret(t *testing.T) {
	t.Parallel()
	const planted = "ghp_Planted/SecretValue99"
	forms := []string{
		base64.StdEncoding.EncodeToString([]byte(planted)),
		base64.RawStdEncoding.EncodeToString([]byte(planted)),
		base64.URLEncoding.EncodeToString([]byte(planted)),
		base64.RawURLEncoding.EncodeToString([]byte(planted)),
	}
	got := promptWith(map[string]string{"GH_TOKEN": planted}, strings.Join(forms, " "), nil)
	for _, form := range forms {
		if strings.Contains(got, form) {
			t.Errorf("base64 form %q leaked:\n%s", form, got)
		}
	}
}

func promptWith(env map[string]string, body string, headers map[string]string) string {
	agent := definition.Agent{
		Name: "implementer",
		Body: "Use {{GH_TOKEN}}.",
	}
	if headers != nil {
		agent.MCPServers = map[string]definition.MCPServer{
			"github": {Headers: headers},
		}
	}
	return BuildPrompt(agent, Context{Title: "T", Body: body, Env: env})
}

func TestBuildPromptHarnessOmitsAuthMaterial(t *testing.T) {
	t.Parallel()
	agent := definition.Agent{
		Name: "reviewer",
		Body: "Review the diff.",
		Harness: &definition.Harness{
			Type:  "claude",
			Model: "claude-5-sonnet-high",
			Auth:  &definition.HarnessAuth{Source: "secret", SecretName: "ANTHROPIC_API_KEY"},
		},
	}
	got := BuildPrompt(agent, Context{Title: "Review", Body: "Look at the PR."})
	if !strings.Contains(got, "Harness: claude\n") || !strings.Contains(got, "Harness model: claude-5-sonnet-high\n") {
		t.Fatalf("harness identity missing:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "SecretName") {
		t.Fatalf("harness auth leaked:\n%s", got)
	}
}

func goldenAgent() definition.Agent {
	return definition.Agent{
		Name:        "implementer",
		Description: "Makes the change and opens a pull request.",
		Model:       "claude-5-opus-high",
		Body:        "# Implement\n\nYou make the change described by the work item and the spec.",
	}
}

func goldenContext() Context {
	return Context{
		WorkItemID:         "wi_01HQAND0FF0000000000000000",
		ConversationID:     "conv_01HQAND0FF0000000000000000",
		Source:             "issue",
		SourceRef:          "acme/payments-docs#17",
		Title:              "Fix the broken documentation link",
		Body:               "The payments docs link to /v1/refunds, which 404s. Point them at /v2/refunds.",
		AcceptanceCriteria: "The doc link returns HTTP 200 and the PR describes the validation.",
		PriorStages: []StageOutput{
			{
				Stage:        "triage",
				Agent:        "triage",
				RunID:        "run_01HQTR1AGE0000000000000000",
				SummaryText:  "Scope is the payments docs refund link. No code change outside docs/.",
				ArtifactURIs: []string{"s3://factory/wi_01HQAND0FF0000000000000000/triage.txt"},
			},
			{
				Stage:       "planning",
				Agent:       "spec",
				RunID:       "run_01HQPLAN000000000000000000",
				SummaryText: "Update docs/refunds.md anchor and add a link check.",
				ArtifactURIs: []string{
					"s3://factory/wi_01HQAND0FF0000000000000000/spec.md",
					"s3://factory/wi_01HQAND0FF0000000000000000/plan.md",
				},
			},
		},
	}
}
