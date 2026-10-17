package outbound

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCardGoldens(t *testing.T) {
	const link = "https://factory.example/api/v1/agent/runs/run_9"
	exitCode := 2
	base := Notice{
		WorkItemID: "task_01M53XGMHEQYS587SZPZWBZHSV",
		RunID:      "run_9",
		Agent:      "implementer",
		Model:      "claude-test",
		Title:      "Emphasize Hello",
		Repo:       "polds/factory",
		Number:     89,
	}
	findings := findingsLine("first nit\nsecond nit", link)
	cases := []struct {
		name     string
		notice   Notice
		findings string
	}{
		{name: "triage", notice: cardNotice(base, KindStageChanged, "triage")},
		{name: "building", notice: func() Notice {
			n := cardNotice(base, KindStageChanged, "building")
			n.PRURL = "https://github.com/polds/factory/pull/77"
			return n
		}()},
		{name: "findings", notice: cardNotice(base, KindFindings, "reviewing"), findings: findings},
		{name: "approval", notice: func() Notice {
			n := cardNotice(base, KindApprovalPending, "planning")
			n.Text = "Ship the spec?"
			return n
		}()},
		{name: "failed", notice: func() Notice {
			n := cardNotice(base, KindFailed, "building")
			n.Text = "tests failed"
			n.ExitCode = &exitCode
			n.PRURL = "https://github.com/polds/factory/pull/77"
			return n
		}()},
		{name: "handoff", notice: func() Notice {
			n := cardNotice(base, KindHandoff, "human_handoff")
			n.PRURL = "https://github.com/polds/factory/pull/77"
			return n
		}()},
		{name: "revision", notice: func() Notice {
			n := cardNotice(base, KindReplyReceived, "building")
			n.Actor = "ada"
			n.PRURL = "https://github.com/polds/factory/pull/77"
			return n
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, blocks := renderCard(tc.notice, link, tc.findings)
			assertBlockLimits(t, blocks)
			got := goldenBytes(t, text, blocks)
			path := filepath.Join("testdata", tc.name+".json")
			if os.Getenv("UPDATE_GOLDENS") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden %s mismatch\n%s", tc.name, got)
			}
		})
	}
}

func TestHeaderTruncates(t *testing.T) {
	n := Notice{
		Kind:       KindStageChanged,
		WorkItemID: "wi_01",
		Stage:      "triage",
		Title:      strings.Repeat("a", 180),
	}
	_, blocks := renderCard(n, "", "")
	header := headerOf(t, blocks)
	if utf8.RuneCountInString(header) != maxHeader {
		t.Fatalf("header len %d", utf8.RuneCountInString(header))
	}
	if !strings.HasSuffix(header, "…") {
		t.Fatalf("header %q", header)
	}
}

func TestBlockLimitsHoldForLongText(t *testing.T) {
	n := Notice{
		Kind:       KindFindings,
		WorkItemID: "wi_01",
		Stage:      "reviewing",
		Title:      strings.Repeat("t", 400),
		Agent:      strings.Repeat("agent", 40),
		Model:      strings.Repeat("model", 40),
		Text:       strings.Repeat("x", 5000) + "\nsecond",
		Actor:      strings.Repeat("u", 400),
		PRURL:      "https://github.com/acme/repo/pull/1",
	}
	link := "https://factory.example/" + strings.Repeat("r", 400)
	_, blocks := renderCard(n, link, findingsLine(n.Text, link))
	assertBlockLimits(t, blocks)
	if strings.Contains(string(blocks), strings.Repeat("x", 301)) {
		t.Fatal("findings prose exceeded 300")
	}
}

func TestThreadReplyStaysWithinSectionLimit(t *testing.T) {
	n := Notice{
		Kind:  KindQuestionAsked,
		Text:  strings.Repeat("q", maxSection+50),
		Agent: "implementer",
		Model: "claude-test",
	}
	_, blocks := renderThreadReply(n, "https://factory.example/api/v1/agent/runs/run_9")
	assertBlockLimits(t, blocks)
}

func cardNotice(base Notice, kind Kind, stage string) Notice {
	base.Kind = kind
	base.Stage = stage
	return base
}

func goldenBytes(t *testing.T, text string, blocks json.RawMessage) []byte {
	t.Helper()
	var parsed any
	if err := json.Unmarshal(blocks, &parsed); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(struct {
		Text   string `json:"text"`
		Blocks any    `json:"blocks"`
	}{Text: text, Blocks: parsed}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func assertBlockLimits(t *testing.T, blocks json.RawMessage) {
	t.Helper()
	var doc []blockView
	if err := json.Unmarshal(blocks, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) == 0 || len(doc) > maxBlocks {
		t.Fatalf("blocks %d", len(doc))
	}
	for _, block := range doc {
		switch block.Type {
		case "header":
			if block.Text == nil || block.Text.Type != "plain_text" || utf8.RuneCountInString(block.Text.Text) > maxHeader {
				t.Fatalf("header %+v", block.Text)
			}
		case "section":
			if block.Text == nil || block.Text.Type != "mrkdwn" || utf8.RuneCountInString(block.Text.Text) > maxSection {
				t.Fatalf("section %+v", block.Text)
			}
			if strings.HasPrefix(block.Text.Text, "Findings:") {
				prose := block.Text.Text
				if i := strings.IndexByte(prose, '\n'); i >= 0 {
					prose = prose[:i]
				}
				if utf8.RuneCountInString(prose) > maxFindings {
					t.Fatalf("findings %d", utf8.RuneCountInString(prose))
				}
			}
		case "context":
			if len(block.Elements) == 0 {
				t.Fatal("empty context")
			}
			for _, el := range block.Elements {
				if utf8.RuneCountInString(el.Text) > maxContext {
					t.Fatalf("context %d", utf8.RuneCountInString(el.Text))
				}
			}
		default:
			t.Fatalf("block type %s", block.Type)
		}
	}
}

type blockView struct {
	Type     string          `json:"type"`
	Text     *blockTextView  `json:"text"`
	Elements []blockTextView `json:"elements"`
}

type blockTextView struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func headerOf(t *testing.T, blocks json.RawMessage) string {
	t.Helper()
	var doc []blockView
	if err := json.Unmarshal(blocks, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) == 0 || doc[0].Type != "header" || doc[0].Text == nil {
		t.Fatalf("header missing: %s", blocks)
	}
	return doc[0].Text.Text
}
