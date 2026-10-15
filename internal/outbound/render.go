package outbound

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"

	"github.com/polds/factory/internal/forge/github"
)

func runLink(externalURL, runID string) string {
	base := strings.TrimRight(strings.TrimSpace(externalURL), "/")
	if base == "" || runID == "" {
		return ""
	}
	return base + "/api/v1/agent/runs/" + url.PathEscape(runID)
}

func attribution(notice Notice) string {
	parts := []string{"Factory"}
	if notice.Agent != "" {
		parts = append(parts, notice.Agent)
	}
	if notice.Model != "" {
		parts = append(parts, notice.Model)
	}
	return strings.Join(parts, " · ")
}

func renderRoot(notice Notice, link string) string {
	var b strings.Builder
	b.WriteString(attribution(notice))
	b.WriteByte('\n')
	title := notice.Title
	if title == "" {
		title = notice.WorkItemID
	}
	fmt.Fprintf(&b, "Work item %s: %s", notice.WorkItemID, title)
	writeLink(&b, link)
	return b.String()
}

func renderStatus(notice Notice, link string) string {
	var b strings.Builder
	b.WriteString(attribution(notice))
	b.WriteByte('\n')
	b.WriteString(statusLine(notice))
	if notice.Title != "" {
		b.WriteByte('\n')
		b.WriteString(notice.Title)
	}
	if notice.Text != "" && notice.Kind != KindStageChanged && notice.Kind != KindRunStarted {
		b.WriteByte('\n')
		b.WriteString(notice.Text)
	}
	writeLink(&b, link)
	return b.String()
}

func renderMilestone(notice Notice, link string) string {
	var b strings.Builder
	b.WriteString(attribution(notice))
	b.WriteByte('\n')
	switch notice.Kind {
	case KindQuestionAsked:
		b.WriteString("Question: ")
		b.WriteString(notice.Text)
		b.WriteString("\nReply in this thread to answer.")
	case KindPROpened:
		b.WriteString("Pull request opened: ")
		b.WriteString(notice.PRURL)
	case KindHandoff:
		b.WriteString("Done.")
	case KindRunStarted, KindStageChanged, KindApprovalPending, KindFindings, KindFailed:
		b.WriteString(statusLine(notice))
	default:
		_ = neverKind(notice.Kind)
	}
	writeLink(&b, link)
	return b.String()
}

func statusLine(notice Notice) string {
	switch notice.Kind {
	case KindRunStarted:
		return "Run started"
	case KindStageChanged:
		return "Stage: " + notice.Stage
	case KindQuestionAsked:
		return "Question asked"
	case KindApprovalPending:
		return "Approval pending"
	case KindPROpened:
		return "Pull request opened"
	case KindFindings:
		return "Findings"
	case KindHandoff:
		return "Done"
	case KindFailed:
		return "Failed"
	default:
		return string(neverKind(notice.Kind))
	}
}

func writeLink(b *strings.Builder, link string) {
	if link == "" {
		return
	}
	b.WriteByte('\n')
	b.WriteString(link)
}

func checkRun(notice Notice, summary, details string) github.CheckRun {
	cr := github.CheckRun{
		Name:       "factory",
		Status:     github.CheckInProgress,
		Summary:    summary,
		DetailsURL: details,
	}
	switch notice.Kind {
	case KindFailed:
		cr.Status = github.CheckCompleted
		cr.Conclusion = "failure"
	case KindHandoff:
		cr.Status = github.CheckCompleted
		cr.Conclusion = "success"
	case KindRunStarted, KindStageChanged, KindQuestionAsked, KindApprovalPending, KindPROpened, KindFindings:
	default:
		_ = neverKind(notice.Kind)
	}
	return cr
}

func rootMarker(workItemID string) string {
	return "<!-- factory:" + workItemID + " -->"
}

func milestoneMarker(notice Notice) string {
	sum := sha256.Sum256([]byte(string(notice.Kind) + "\n" + notice.RunID + "\n" + notice.Text + "\n" + notice.PRURL))
	return fmt.Sprintf("<!-- factory:%s:%s:%x -->", notice.WorkItemID, notice.Kind, sum[:6])
}
