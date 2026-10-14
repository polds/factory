package handoff

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
)

// Agent is the resolved stage agent. BuildPrompt reads its name, description,
// model or harness, and instruction body. Secret names, MCP server material,
// and harness auth are not copied into the prompt.
type Agent = definition.Agent

// StageOutput is one completed stage's contribution to the next prompt.
type StageOutput struct {
	Stage        string
	Agent        string
	RunID        string
	SummaryText  string
	ArtifactURIs []string
}

// Context is the work-item state packaged into a stage prompt.
type Context struct {
	WorkItemID         string
	ConversationID     string
	Source             string
	SourceRef          string
	Title              string
	Body               string
	PriorStages        []StageOutput
	AcceptanceCriteria string
	// Env is the stage's resolved environment, including secret material.
	// BuildPrompt never copies it. Values are redacted from included text.
	// {{SECRET_NAME}} references are names, not material, and are left alone.
	// Values shorter than 4 bytes are not redacted from text that already
	// contained them; they are still omitted as environment entries.
	Env map[string]string
}

var (
	secretRefExact = regexp.MustCompile(`^\{\{[A-Z][A-Z0-9_]*\}\}$`)
	secretRefAuth  = regexp.MustCompile(`(?i)^(bearer|basic)\s+\{\{[A-Z][A-Z0-9_]*\}\}$`)
	authValue      = regexp.MustCompile(`(?i)^(bearer|basic)\s+(\S+)\s*$`)
)

// BuildPrompt returns the deterministic FACTORY_TASK_PROMPT for agent and ctx.
// Prior stage outputs stay in slice order. Secret values from ctx.Env and
// from the agent's MCP server env and headers do not appear in the result.
func BuildPrompt(agent Agent, ctx Context) string {
	var b strings.Builder
	writeLine(&b, "Agent", agent.Name)
	if agent.Description != "" {
		writeLine(&b, "Description", agent.Description)
	}
	if agent.Model != "" {
		writeLine(&b, "Model", agent.Model)
	}
	if agent.Harness != nil {
		writeLine(&b, "Harness", agent.Harness.Type)
		if agent.Harness.Model != "" {
			writeLine(&b, "Harness model", agent.Harness.Model)
		}
	}
	b.WriteByte('\n')
	b.WriteString("# Instructions\n")
	writeBody(&b, agent.Body)
	b.WriteString("\n# Work item\n")
	writeLine(&b, "Work item", ctx.WorkItemID)
	writeLine(&b, "Conversation", ctx.ConversationID)
	writeLine(&b, "Source", ctx.Source)
	writeLine(&b, "Source ref", ctx.SourceRef)
	writeLine(&b, "Title", ctx.Title)
	b.WriteByte('\n')
	writeBody(&b, ctx.Body)
	b.WriteString("\n# Acceptance criteria\n")
	writeBody(&b, ctx.AcceptanceCriteria)
	b.WriteString("\n# Prior stage outputs\n")
	if len(ctx.PriorStages) == 0 {
		b.WriteString("(none)\n")
	} else {
		for i, stage := range ctx.PriorStages {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "## %d. %s\n", i+1, stage.Stage)
			writeLine(&b, "Agent", stage.Agent)
			writeLine(&b, "Run", stage.RunID)
			b.WriteString("Summary:\n")
			writeBody(&b, stage.SummaryText)
			b.WriteString("Artifacts:\n")
			if len(stage.ArtifactURIs) == 0 {
				b.WriteString("(none)\n")
				continue
			}
			for _, uri := range stage.ArtifactURIs {
				b.WriteString("- ")
				b.WriteString(uri)
				b.WriteByte('\n')
			}
		}
	}
	return harness.Redact(b.String(), secretValues(agent, ctx))
}

func writeLine(b *strings.Builder, label, value string) {
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeBody(b *strings.Builder, value string) {
	if strings.TrimSpace(value) == "" {
		b.WriteString("(none)\n")
		return
	}
	b.WriteString(value)
	if !strings.HasSuffix(value, "\n") {
		b.WriteByte('\n')
	}
}

func secretValues(agent Agent, ctx Context) []string {
	var vals []string
	for _, v := range ctx.Env {
		vals = append(vals, secretMaterial(v)...)
	}
	for _, srv := range agent.MCPServers {
		for _, v := range srv.Env {
			vals = append(vals, secretMaterial(v)...)
		}
		for _, v := range srv.Headers {
			vals = append(vals, secretMaterial(v)...)
		}
	}
	return vals
}

func secretMaterial(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || secretRefExact.MatchString(v) || secretRefAuth.MatchString(v) {
		return nil
	}
	out := []string{v}
	if m := authValue.FindStringSubmatch(v); m != nil {
		rest := m[2]
		if rest != v && !secretRefExact.MatchString(rest) {
			out = append(out, rest)
		}
	}
	return out
}
