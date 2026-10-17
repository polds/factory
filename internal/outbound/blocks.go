package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Block Kit limits that renderCard stays inside.
// Header and section limits are the documented Slack maxima.
// Findings prose is capped tighter than a section so the card never
// dumps a review.
const (
	maxBlocks   = 50
	maxHeader   = 150
	maxSection  = 3000
	maxFindings = 300
	maxContext  = 75
)

var stageOrder = []struct {
	id    string
	label string
}{
	{"triage", "Triage"},
	{"planning", "Planning"},
	{"building", "Building"},
	{"reviewing", "Reviewing"},
	{"human_handoff", "Handoff"},
}

type textObj struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type headerBlock struct {
	Type string  `json:"type"`
	Text textObj `json:"text"`
}

type sectionBlock struct {
	Type string  `json:"type"`
	Text textObj `json:"text"`
}

type contextBlock struct {
	Type     string    `json:"type"`
	Elements []textObj `json:"elements"`
}

// renderCard is the one Slack status card and its plain-text fallback.
// findings is the already-short findings line, empty when there are none.
func renderCard(notice Notice, link, findings string) (string, json.RawMessage) {
	text := renderSummary(notice, link, findings)
	raw := mustBlocks(cardBlocks(notice, link, findings))
	return text, raw
}

// renderThreadReply is one actionable thread post and its text fallback.
func renderThreadReply(notice Notice, link string) (string, json.RawMessage) {
	body := threadBody(notice)
	var b strings.Builder
	b.WriteString(attribution(notice))
	b.WriteByte('\n')
	b.WriteString(body)
	writeLink(&b, link)
	raw := mustBlocks([]any{sectionBlock{
		Type: "section",
		Text: textObj{Type: "mrkdwn", Text: truncate(mrkdwnBody(notice, body), maxSection)},
	}})
	return b.String(), raw
}

func renderSummary(notice Notice, link, findings string) string {
	var b strings.Builder
	b.WriteString(attribution(notice))
	b.WriteByte('\n')
	title := strings.TrimSpace(notice.Title)
	if title != "" {
		b.WriteString(title)
		b.WriteByte('\n')
	}
	b.WriteString(progressLine(notice))
	b.WriteByte('\n')
	b.WriteString(statusLine(notice))
	if findings != "" {
		b.WriteByte('\n')
		b.WriteString(findings)
	}
	if line := revisionLine(notice.Actor); line != "" && notice.Kind != KindReplyReceived {
		b.WriteByte('\n')
		b.WriteString(line)
	}
	writeLink(&b, link)
	return b.String()
}

func cardBlocks(notice Notice, link, findings string) []any {
	var blocks []any
	blocks = append(blocks, headerBlock{
		Type: "header",
		Text: textObj{Type: "plain_text", Text: headerText(notice)},
	})
	blocks = append(blocks, textBlock(sourceLine(notice)))
	blocks = append(blocks, sectionBlock{
		Type: "section",
		Text: textObj{Type: "mrkdwn", Text: truncate(progressLine(notice), maxSection)},
	})
	if line := cardStatus(notice); line != "" {
		blocks = append(blocks, sectionBlock{
			Type: "section",
			Text: textObj{Type: "mrkdwn", Text: truncate(escapeMrkdwn(line), maxSection)},
		})
	}
	blocks = append(blocks, textBlock(attribution(notice)))
	if findings != "" {
		blocks = append(blocks, sectionBlock{
			Type: "section",
			Text: textObj{Type: "mrkdwn", Text: truncate(findings, maxSection)},
		})
	}
	if line := revisionLine(notice.Actor); line != "" {
		blocks = append(blocks, sectionBlock{
			Type: "section",
			Text: textObj{Type: "mrkdwn", Text: truncate(escapeMrkdwn(line), maxSection)},
		})
	}
	if links := linksLine(link, notice.PRURL); links != "" {
		blocks = append(blocks, sectionBlock{
			Type: "section",
			Text: textObj{Type: "mrkdwn", Text: truncate(links, maxSection)},
		})
	}
	return blocks
}

// textBlock uses a context block when the line fits Slack's context
// element cap, and a section otherwise so a long model id is not cut.
func textBlock(line string) any {
	line = strings.TrimSpace(line)
	if line == "" {
		line = "Factory"
	}
	if utf8.RuneCountInString(line) <= maxContext {
		return contextBlock{
			Type:     "context",
			Elements: []textObj{{Type: "mrkdwn", Text: line}},
		}
	}
	return sectionBlock{
		Type: "section",
		Text: textObj{Type: "mrkdwn", Text: truncate(line, maxSection)},
	}
}

// cardStatus is the line the progress row does not already show.
// Stage changes and handoff stay on the progress row. A failure or a
// pending approval has to be readable on the card itself.
func cardStatus(notice Notice) string {
	switch notice.Kind {
	case KindFailed, KindApprovalPending:
		return statusLine(notice)
	case KindRunStarted, KindStageChanged, KindQuestionAsked, KindPROpened, KindFindings, KindHandoff, KindReplyReceived:
		return ""
	default:
		return statusLine(Notice{Kind: neverKind(notice.Kind)})
	}
}

func headerText(notice Notice) string {
	title := strings.TrimSpace(notice.Title)
	if title == "" {
		title = "Work item " + shortID(notice.WorkItemID)
	}
	return truncate(title, maxHeader)
}

func progressLine(notice Notice) string {
	current := stageIndex(notice.Stage)
	parts := make([]string, 0, len(stageOrder))
	for i, stage := range stageOrder {
		mark := "⬜"
		switch {
		case notice.Kind == KindHandoff || (current >= 0 && i < current):
			mark = "✅"
		case current >= 0 && i == current:
			mark = "⏳"
		}
		parts = append(parts, mark+" "+stage.label)
	}
	return strings.Join(parts, "  →  ")
}

func stageIndex(stage string) int {
	for i, item := range stageOrder {
		if item.id == stage {
			return i
		}
	}
	return -1
}

func sourceLine(notice Notice) string {
	label, href := sourceRef(notice)
	short := shortID(notice.WorkItemID)
	switch {
	case href != "" && short != "":
		return fmt.Sprintf("<%s|%s> · %s", href, escapeMrkdwn(label), escapeMrkdwn(short))
	case href != "":
		return fmt.Sprintf("<%s|%s>", href, escapeMrkdwn(label))
	case label != "" && short != "":
		return escapeMrkdwn(label) + " · " + escapeMrkdwn(short)
	case short != "":
		return escapeMrkdwn(short)
	default:
		return "work item"
	}
}

func sourceRef(notice Notice) (label, href string) {
	if notice.Repo != "" && notice.Number > 0 {
		return notice.Repo + "#" + strconv.Itoa(notice.Number),
			"https://github.com/" + notice.Repo + "/issues/" + strconv.Itoa(notice.Number)
	}
	if n, ok := pullNumber(notice.PRURL); ok {
		return "PR #" + strconv.Itoa(n), notice.PRURL
	}
	if notice.IssueID != "" {
		return notice.IssueID, ""
	}
	return "", ""
}

func linksLine(link, pr string) string {
	var parts []string
	if link != "" {
		parts = append(parts, "<"+link+"|Open run>")
	}
	if pr != "" {
		parts = append(parts, "<"+pr+"|Open PR>")
	}
	return strings.Join(parts, "  ·  ")
}

func findingsLine(text, link string) string {
	lines := nonEmptyLines(text)
	if len(lines) == 0 {
		return ""
	}
	prose := fmt.Sprintf("Findings: %d", len(lines))
	preview := oneLine(lines[0])
	if preview != "" {
		prose += " — " + preview
	}
	prose = truncate(prose, maxFindings)
	if link == "" {
		return prose
	}
	return prose + "\n" + link
}

func revisionLine(actor string) string {
	actor = oneLine(strings.TrimSpace(actor))
	if actor == "" {
		return ""
	}
	return "Revision requested by " + actor
}

func ackLine(notice Notice) string {
	if n, ok := pullNumber(notice.PRURL); ok {
		return fmt.Sprintf("Got it — revising on PR #%d", n)
	}
	return "Got it — working on it"
}

func failedLine(notice Notice) string {
	reason := oneLine(strings.TrimSpace(notice.Text))
	if reason == "" {
		reason = "the run failed"
	}
	line := "Failed: " + reason
	if notice.ExitCode != nil {
		line = fmt.Sprintf("Failed: %s (exit %d)", reason, *notice.ExitCode)
	}
	return truncate(line, maxSection)
}

func threadBody(notice Notice) string {
	switch notice.Kind {
	case KindQuestionAsked:
		text := strings.TrimSpace(notice.Text)
		if text == "" {
			text = "A question needs an answer."
		}
		return "Question: " + text + "\nReply in this thread to answer."
	case KindApprovalPending:
		if strings.TrimSpace(notice.Text) == "" {
			return "Approval pending."
		}
		return "Approval pending.\n" + oneLine(notice.Text)
	case KindPROpened:
		return "Pull request opened: " + notice.PRURL
	case KindFailed:
		return failedLine(notice)
	case KindHandoff:
		return "Done."
	case KindReplyReceived:
		return ackLine(notice)
	case KindRunStarted, KindStageChanged, KindFindings:
		return statusLine(notice)
	default:
		return statusLine(Notice{Kind: neverKind(notice.Kind)})
	}
}

func mrkdwnBody(notice Notice, body string) string {
	switch notice.Kind {
	case KindPROpened:
		if notice.PRURL == "" {
			return escapeMrkdwn(body)
		}
		return "Pull request opened: <" + notice.PRURL + "|" + escapeMrkdwn(notice.PRURL) + ">"
	case KindQuestionAsked, KindApprovalPending, KindFailed, KindReplyReceived, KindHandoff:
		return escapeMrkdwn(body)
	case KindRunStarted, KindStageChanged, KindFindings:
		return escapeMrkdwn(body)
	default:
		_ = neverKind(notice.Kind)
		return escapeMrkdwn(body)
	}
}

func shortID(id string) string {
	const max = 14
	r := []rune(id)
	if len(r) <= max {
		return id
	}
	return string(r[:8]) + "…" + string(r[len(r)-4:])
}

func truncate(s string, max int) string {
	r := []rune(s)
	if max <= 0 {
		return ""
	}
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func pullNumber(raw string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 4 || parts[len(parts)-2] != "pull" {
		return 0, false
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func mustBlocks(blocks []any) json.RawMessage {
	if len(blocks) > maxBlocks {
		blocks = blocks[:maxBlocks]
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// legacyStatus reports whether ref is a Slack message ts from a thread
// that still has a separate status message.
func legacyStatus(ref string) bool {
	i := strings.IndexByte(ref, '.')
	if i <= 0 || i == len(ref)-1 {
		return false
	}
	return allDigits(ref[:i]) && allDigits(ref[i+1:])
}

// cardMemory is the durable Slack card state keyed by work item.
// Acked lists follow-up run ids whose thread ack was already posted.
type cardMemory struct {
	Acked    []string `json:"acked,omitempty"`
	Actor    string   `json:"actor,omitempty"`
	PRURL    string   `json:"pr_url,omitempty"`
	Findings string   `json:"findings,omitempty"`
}

func (m cardMemory) acked(runID string) bool {
	for _, id := range m.Acked {
		if id == runID {
			return true
		}
	}
	return false
}

func applyCard(notice Notice, mem cardMemory) Notice {
	if notice.PRURL == "" {
		notice.PRURL = mem.PRURL
	}
	if notice.Actor == "" {
		notice.Actor = mem.Actor
	}
	return notice
}

func rememberCard(mem cardMemory, notice Notice, findings string) (cardMemory, bool) {
	changed := false
	if notice.PRURL != "" && notice.PRURL != mem.PRURL {
		mem.PRURL = notice.PRURL
		changed = true
	}
	if notice.Actor != "" && notice.Actor != mem.Actor {
		mem.Actor = notice.Actor
		changed = true
	}
	if notice.Kind == KindFindings && findings != mem.Findings {
		mem.Findings = findings
		changed = true
	}
	return mem, changed
}

func (n *Notifier) loadCard(ctx context.Context, workItemID string) (cardMemory, error) {
	thread, err := n.load(ctx, workItemID, cardProvider)
	if err != nil {
		return cardMemory{}, err
	}
	if strings.TrimSpace(thread.RootRef) == "" {
		return cardMemory{}, nil
	}
	var mem cardMemory
	if json.Unmarshal([]byte(thread.RootRef), &mem) != nil {
		mem = cardMemory{}
	}
	return mem, nil
}

func (n *Notifier) saveCard(ctx context.Context, workItemID string, mem cardMemory) error {
	raw, err := json.Marshal(mem)
	if err != nil {
		return err
	}
	return n.save(ctx, Thread{
		WorkItemID: workItemID,
		Provider:   cardProvider,
		Channel:    "card",
		RootRef:    string(raw),
	})
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
