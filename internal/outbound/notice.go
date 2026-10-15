package outbound

import (
	"errors"
	"fmt"
)

// Kind is one outbound event. T30 maps orchestrator and run events onto these.
type Kind string

const (
	KindRunStarted      Kind = "run_started"
	KindStageChanged    Kind = "stage_changed"
	KindQuestionAsked   Kind = "question_asked"
	KindApprovalPending Kind = "approval_pending"
	KindPROpened        Kind = "pr_opened"
	KindFindings        Kind = "findings"
	KindHandoff         Kind = "handoff"
	KindFailed          Kind = "failed"
)

// Notice is one work-item event to deliver.
// Channel overrides Config.SlackChannel for this event.
// Repo and Number select the GitHub issue or pull request (owner/name and
// the issue number). HeadSHA opens the check run; later events keep that
// check run even when the SHA changes.
// IssueID selects the Linear issue.
// SlackTeam and ConversationID bind the Slack root for reply continuation.
type Notice struct {
	Kind           Kind
	WorkItemID     string
	RunID          string
	Agent          string
	Model          string
	Stage          string
	Title          string
	Text           string
	PRURL          string
	HeadSHA        string
	Repo           string
	Number         int
	IssueID        string
	Channel        string
	SlackTeam      string
	ConversationID string
}

func (n Notice) valid() error {
	if n.WorkItemID == "" {
		return errors.New("outbound: work item is required")
	}
	if !n.Kind.valid() {
		return fmt.Errorf("outbound: kind %q is not valid", n.Kind)
	}
	return nil
}

func (k Kind) valid() bool {
	switch k {
	case KindRunStarted, KindStageChanged, KindQuestionAsked, KindApprovalPending,
		KindPROpened, KindFindings, KindHandoff, KindFailed:
		return true
	default:
		return false
	}
}

func milestone(k Kind) bool {
	switch k {
	case KindQuestionAsked, KindPROpened, KindHandoff:
		return true
	case KindRunStarted, KindStageChanged, KindApprovalPending, KindFindings, KindFailed:
		return false
	default:
		_ = neverKind(k)
		return false
	}
}

func neverKind(k Kind) Kind { return k }
