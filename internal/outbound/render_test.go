package outbound

import (
	"strings"
	"testing"

	"github.com/polds/factory/internal/forge/github"
)

func TestRenderNamesAgentModelAndRun(t *testing.T) {
	notice := Notice{
		WorkItemID: "wi_01",
		RunID:      "run_9",
		Agent:      "implementer",
		Model:      "claude-test",
		Title:      "Fix the parser",
		Stage:      "building",
		Text:       "what should an empty name do?",
		PRURL:      "https://github.com/polds/factory/pull/9",
	}
	link := runLink("https://factory.example/", notice.RunID)
	const wantLink = "https://factory.example/api/v1/agent/runs/run_9"
	if link != wantLink {
		t.Fatalf("link %s", link)
	}
	if runLink("", "run_9") != "" || runLink("https://factory.example", "") != "" {
		t.Fatal("empty link")
	}

	kinds := []Kind{
		KindRunStarted, KindStageChanged, KindQuestionAsked, KindApprovalPending,
		KindPROpened, KindFindings, KindHandoff, KindFailed,
	}
	for _, kind := range kinds {
		notice.Kind = kind
		for _, text := range []string{renderRoot(notice, link), renderStatus(notice, link)} {
			assertAttribution(t, text, wantLink)
		}
		if milestone(kind) {
			assertAttribution(t, renderMilestone(notice, link), wantLink)
		}
		cr := checkRun(notice, renderStatus(notice, link), link)
		if cr.Name != "factory" || cr.DetailsURL != link {
			t.Fatalf("check %#v", cr)
		}
		switch kind {
		case KindFailed:
			if cr.Status != github.CheckCompleted || cr.Conclusion != "failure" {
				t.Fatalf("failed check %#v", cr)
			}
		case KindHandoff:
			if cr.Status != github.CheckCompleted || cr.Conclusion != "success" {
				t.Fatalf("done check %#v", cr)
			}
		case KindRunStarted, KindStageChanged, KindQuestionAsked, KindApprovalPending, KindPROpened, KindFindings:
			if cr.Status != github.CheckInProgress || cr.Conclusion != "" {
				t.Fatalf("open check %#v", cr)
			}
		default:
			t.Fatalf("kind %s", neverKind(kind))
		}
	}

	notice.Kind = KindQuestionAsked
	if !strings.Contains(renderMilestone(notice, link), "Reply in this thread to answer.") {
		t.Fatalf("question %s", renderMilestone(notice, link))
	}
	notice.Kind = KindPROpened
	if !strings.Contains(renderMilestone(notice, link), notice.PRURL) {
		t.Fatalf("pr %s", renderMilestone(notice, link))
	}
	notice.Kind = KindHandoff
	if !strings.Contains(renderMilestone(notice, link), "Done.") {
		t.Fatalf("done %s", renderMilestone(notice, link))
	}
	if rootMarker("wi_01") != "<!-- factory:wi_01 -->" {
		t.Fatal(rootMarker("wi_01"))
	}
	again := milestoneMarker(notice)
	if milestoneMarker(notice) != again {
		t.Fatal("marker changed")
	}
	other := notice
	other.Text = "different"
	if milestoneMarker(other) == again {
		t.Fatal("marker ignored the text")
	}
}

func assertAttribution(t *testing.T, text, link string) {
	t.Helper()
	for _, want := range []string{"Factory · implementer · claude-test", link} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}
