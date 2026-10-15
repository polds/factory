package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/orchestrator"
)

type replyKind int

const (
	replyNone replyKind = iota
	replySlack
	replyGitHub
)

// continueReply runs with e.mu held.
func (e *Engine) continueReply(ctx context.Context, tree definition.Tree, ev event.Event, ref Ref, kind replyKind) (Result, *event.Event, error) {
	allowed, err := e.eligible(ctx, kind, ev)
	if err != nil {
		return Result{}, nil, err
	}
	if !allowed {
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindIgnored,
			WorkItemID:     ref.WorkItemID,
			ConversationID: ref.ConversationID,
		}}}, nil, nil
	}
	if !e.claim(claimKey("\x00reply", ev.DeliveryID)) {
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindDuplicate,
			WorkItemID:     ref.WorkItemID,
			ConversationID: ref.ConversationID,
		}}}, nil, nil
	}
	item, err := e.Store.GetWorkItem(ctx, ref.WorkItemID)
	if err != nil {
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: work item: %w", err)
	}
	if item.Status == orchestrator.StatusCancelled {
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindIgnored,
			WorkItemID:     item.ID,
			ConversationID: item.ConversationID,
		}}}, nil, nil
	}
	_, err = e.Store.PendingInput(ctx, item.ID)
	switch {
	case err == nil:
		answered, ansErr := e.Store.AnswerInput(ctx, item.ID, replyText(ev), actorName(ev))
		if ansErr != nil {
			e.unclaimReply(ev.DeliveryID)
			return Result{}, nil, ansErr
		}
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindAnswered,
			WorkItemID:     item.ID,
			ConversationID: item.ConversationID,
			RunID:          answered.ID,
		}}}, nil, nil
	case errors.Is(err, ErrNotFound):
	default:
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: pending input: %w", err)
	}
	if terminalHandoff(item) {
		pr, ok, prErr := e.Store.OpenPullRequest(ctx, item.ID)
		if prErr != nil {
			e.unclaimReply(ev.DeliveryID)
			return Result{}, nil, fmt.Errorf("automation: pull request: %w", prErr)
		}
		if ok && pr != "" {
			return e.followUp(ctx, tree, ev, item, pr)
		}
	}
	if err := e.Store.AppendContext(ctx, item.ID, laterNote(ev)); err != nil {
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: append context: %w", err)
	}
	return Result{Matched: true, Outcomes: []Outcome{{
		Kind:           KindContinued,
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
	}}}, nil, nil
}

func (e *Engine) followUp(ctx context.Context, tree definition.Tree, ev event.Event, item WorkItem, pullRequest string) (Result, *event.Event, error) {
	next := item
	next.Stage = orchestrator.StageBuilding
	next.Status = orchestrator.StatusOpen
	agent, model, pool := stageIdentity(tree, orchestrator.StageBuilding)
	prior, err := e.Store.Context(ctx, item.ID)
	if err != nil {
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: context: %w", err)
	}
	parent := ""
	latest, err := e.Store.LatestRun(ctx, item.ID)
	switch {
	case err == nil:
		parent = latest.ID
	case errors.Is(err, ErrNotFound):
	default:
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: latest run: %w", err)
	}
	run, err := e.Store.CreateRun(ctx, NewRun{
		FactoryID:      item.FactoryID,
		Agent:          agent,
		Pool:           pool,
		Priority:       PriorityPipeline,
		Prompt:         followUpPrompt(next, prior, eventFence(ev), pullRequest),
		Title:          item.Title,
		WorkItemID:     item.ID,
		ParentRunID:    parent,
		ConversationID: item.ConversationID,
		Stage:          orchestrator.StageBuilding,
		ModelID:        model,
	})
	if err != nil {
		e.unclaimReply(ev.DeliveryID)
		return Result{}, nil, fmt.Errorf("automation: create run: %w", err)
	}
	updated, err := e.Store.UpdateWorkItemStage(ctx, item.ID, orchestrator.StageBuilding, orchestrator.StatusOpen)
	if err != nil {
		return Result{}, nil, fmt.Errorf("automation: update work item: %w", err)
	}
	emitted, err := e.stageEvent(ev, updated, chain(ev, e.ownerNames(updated.ID)))
	if err != nil {
		return Result{}, nil, err
	}
	return Result{Matched: true, Outcomes: []Outcome{{
		Kind:           KindFollowUp,
		WorkItemID:     run.WorkItemID,
		ConversationID: run.ConversationID,
		RunID:          run.ID,
		Stage:          run.Stage,
		Agent:          run.Agent,
		ModelID:        run.ModelID,
		Pool:           run.Pool,
	}}}, &emitted, nil
}

func (e *Engine) unclaimReply(deliveryID string) {
	delete(e.claims, claimKey("\x00reply", deliveryID))
}

func (e *Engine) eligible(ctx context.Context, kind replyKind, ev event.Event) (bool, error) {
	if e.bot(ev) {
		return false, nil
	}
	switch kind {
	case replySlack:
		return e.slackUserOK(ev), nil
	case replyGitHub:
		return e.githubUserOK(ctx, ev)
	case replyNone:
		return false, nil
	default:
		panic(fmt.Sprintf("automation: unhandled reply kind %d", neverReply(kind)))
	}
}

func replyShape(tree definition.Tree, ev event.Event) replyKind {
	switch {
	case ev.Provider == event.ProviderSlack && ev.Event == "message_posted" && autoRespond(tree) && threadedReply(ev.Raw):
		return replySlack
	case ev.Provider == event.ProviderGitHub && (ev.Event == "issue_commented" || ev.Event == "pull_request_commented"):
		return replyGitHub
	default:
		return replyNone
	}
}

func autoRespond(tree definition.Tree) bool {
	for _, in := range tree.Factory.Integrations {
		if in.Type == "slack" && in.Slack != nil && in.Slack.AutoRespondToThreadReplies {
			return true
		}
	}
	return false
}

func threadedReply(raw json.RawMessage) bool {
	var body struct {
		ThreadTS string `json:"thread_ts"`
		TS       string `json:"ts"`
		Event    struct {
			ThreadTS string `json:"thread_ts"`
			TS       string `json:"ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	thread, ts := body.ThreadTS, body.TS
	if thread == "" {
		thread, ts = body.Event.ThreadTS, body.Event.TS
	}
	return thread != "" && thread != ts
}

func (e *Engine) bot(ev event.Event) bool {
	if strings.Contains(strings.ToLower(ev.Actor.Handle), "[bot]") {
		return true
	}
	if ev.Actor.ProviderID != "" && contains(e.Allow.BotUserIDs, ev.Actor.ProviderID) {
		return true
	}
	var body struct {
		BotID   string `json:"bot_id"`
		User    string `json:"user"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(ev.Raw, &body); err != nil {
		return false
	}
	if body.BotID != "" || body.Subtype == "bot_message" {
		return true
	}
	return body.User != "" && contains(e.Allow.BotUserIDs, body.User)
}

func (e *Engine) slackUserOK(ev event.Event) bool {
	if len(e.Allow.SlackUserIDs) == 0 {
		return true
	}
	return contains(e.Allow.SlackUserIDs, slackUser(ev))
}

func slackUser(ev event.Event) string {
	if ev.Actor.ProviderID != "" {
		return ev.Actor.ProviderID
	}
	var body struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(ev.Raw, &body); err != nil {
		return ""
	}
	return body.User
}

func (e *Engine) githubUserOK(ctx context.Context, ev event.Event) (bool, error) {
	if e.Allow.GitHub == nil {
		return true, nil
	}
	ok, err := e.Allow.GitHub(ctx, ev.Subject.Repo.Owner, ev.Subject.Repo.Name, ev.Actor.Handle)
	if err != nil {
		return false, fmt.Errorf("automation: github eligibility: %w", err)
	}
	return ok, nil
}

func replyText(ev event.Event) string {
	if ev.Subject.Text != "" {
		return ev.Subject.Text
	}
	var body struct {
		Text    string `json:"text"`
		Comment struct {
			Body string `json:"body"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(ev.Raw, &body); err != nil {
		return ""
	}
	if body.Text != "" {
		return body.Text
	}
	return body.Comment.Body
}

func terminalHandoff(item WorkItem) bool {
	if item.Status == orchestrator.StatusCancelled || item.Status == orchestrator.StatusAwaitingApproval || item.Status == orchestrator.StatusAwaitingInput {
		return false
	}
	return item.Status == orchestrator.StatusComplete || item.Stage == orchestrator.StageHumanHandoff
}

// neverReply forces a compile error when a new replyKind is not handled.
func neverReply(kind replyKind) replyKind { return kind }
