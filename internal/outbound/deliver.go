package outbound

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/polds/factory/internal/forge/github"
)

func itemKey(provider, workItemID string) string {
	return provider + "\x00" + workItemID
}

func slotKey(item, part string) string {
	return item + "\x00" + part
}

func itemKeyOf(key string) string {
	if i := strings.LastIndex(key, "\x00"); i >= 0 {
		return key[:i]
	}
	return key
}

func (n *Notifier) deliverSlack(ctx context.Context, notice Notice) error {
	if n.slack == nil {
		return nil
	}
	channel := notice.Channel
	if channel == "" {
		channel = n.slackChannel
	}
	if channel == "" {
		return nil
	}
	key := itemKey(ProviderSlack, notice.WorkItemID)
	unlock := n.lock(key)
	defer unlock()

	thread, err := n.load(ctx, notice.WorkItemID, ProviderSlack)
	if err != nil {
		return err
	}
	if thread.Channel != "" {
		channel = thread.Channel
	}
	mem, err := n.loadCard(ctx, notice.WorkItemID)
	if err != nil {
		return err
	}
	notice = applyCard(notice, mem)
	link := n.link(notice.RunID)
	findings := mem.Findings
	if notice.Kind == KindFindings {
		findings = findingsLine(notice.Text, link)
	}
	text, blocks := renderCard(notice, link, findings)
	fresh := false
	if thread.RootRef == "" {
		var ts string
		if err := n.retry(ctx, func(ctx context.Context) error {
			var postErr error
			ts, postErr = n.slack.Post(ctx, channel, "", text, blocks)
			return postErr
		}); err != nil {
			return err
		}
		thread.Channel = channel
		thread.RootRef = ts
		if err := n.save(ctx, thread); err != nil {
			return err
		}
		n.markSent(slotKey(key, "card"))
		n.log.Info("outbound posted", "work_item", notice.WorkItemID, "provider", ProviderSlack, "kind", "root")
		fresh = true
	}
	if err := n.bindSlack(ctx, notice, channel, thread.RootRef); err != nil {
		return err
	}
	if !fresh {
		target := thread.RootRef
		slot := "card"
		if legacyStatus(thread.StatusMsgRef) {
			target = thread.StatusMsgRef
			slot = "status"
		}
		ts := target
		if err := n.edit(ctx, slotKey(key, slot), func(ctx context.Context) error {
			return n.slack.Update(ctx, channel, ts, text, blocks)
		}); err != nil {
			return err
		}
	}
	mem, changed := rememberCard(mem, notice, findings)
	if slackReply(notice.Kind) {
		if notice.Kind == KindReplyReceived && notice.RunID != "" && mem.acked(notice.RunID) {
			if changed {
				return n.saveCard(ctx, notice.WorkItemID, mem)
			}
			return nil
		}
		reply, replyBlocks := renderThreadReply(notice, link)
		root := thread.RootRef
		if err := n.retry(ctx, func(ctx context.Context) error {
			_, postErr := n.slack.Post(ctx, channel, root, reply, replyBlocks)
			return postErr
		}); err != nil {
			return err
		}
		n.log.Info("outbound posted", "work_item", notice.WorkItemID, "provider", ProviderSlack, "kind", string(notice.Kind))
		if notice.Kind == KindReplyReceived && notice.RunID != "" && !mem.acked(notice.RunID) {
			mem.Acked = append(mem.Acked, notice.RunID)
			changed = true
		}
	}
	if changed {
		return n.saveCard(ctx, notice.WorkItemID, mem)
	}
	return nil
}

func (n *Notifier) bindSlack(ctx context.Context, notice Notice, channel, rootTS string) error {
	if n.refs == nil || notice.SlackTeam == "" || notice.ConversationID == "" || rootTS == "" {
		return nil
	}
	ref := "slack:" + notice.SlackTeam + "/" + channel + "/" + rootTS
	return n.retry(ctx, func(ctx context.Context) error {
		got, created, err := n.refs.BindExternalRef(ctx, ProviderSlack, ref, notice.WorkItemID, notice.ConversationID)
		if err != nil {
			return err
		}
		if !created && got.WorkItemID != notice.WorkItemID {
			n.log.Warn("outbound slack thread already bound",
				"work_item", notice.WorkItemID,
				"bound_work_item", got.WorkItemID,
			)
		}
		return nil
	})
}

func (n *Notifier) deliverGitHub(ctx context.Context, notice Notice) error {
	if n.github == nil {
		return nil
	}
	key := itemKey(ProviderGitHub, notice.WorkItemID)
	unlock := n.lock(key)
	defer unlock()

	thread, err := n.load(ctx, notice.WorkItemID, ProviderGitHub)
	if err != nil {
		return err
	}
	repo, number := notice.Repo, notice.Number
	if repo == "" || number <= 0 {
		if parsedRepo, parsedNumber, ok := parseGitHubChannel(thread.Channel); ok {
			if repo == "" {
				repo = parsedRepo
			}
			if number <= 0 {
				number = parsedNumber
			}
		}
	}
	if repo == "" || number <= 0 {
		return nil
	}
	link := n.link(notice.RunID)
	marker := rootMarker(notice.WorkItemID)
	if thread.RootRef == "" {
		text := renderRoot(notice, link)
		if err := n.retry(ctx, func(ctx context.Context) error {
			tok, err := n.token(ctx, repo)
			if err != nil {
				return err
			}
			return n.github.UpsertIssueComment(ctx, tok, repo, number, marker, text)
		}); err != nil {
			return err
		}
		thread.Channel = formatGitHubChannel(repo, number)
		thread.RootRef = marker
		if err := n.save(ctx, thread); err != nil {
			return err
		}
		n.markSent(slotKey(key, "comment"))
		n.log.Info("outbound posted", "work_item", notice.WorkItemID, "provider", ProviderGitHub, "kind", "root")
	} else if err := n.edit(ctx, slotKey(key, "comment"), func(ctx context.Context) error {
		tok, err := n.token(ctx, repo)
		if err != nil {
			return err
		}
		return n.github.UpsertIssueComment(ctx, tok, repo, number, marker, renderStatus(notice, link))
	}); err != nil {
		return err
	}
	if err := n.syncCheck(ctx, &thread, key, notice, repo, link); err != nil {
		return err
	}
	if !milestone(notice.Kind) {
		return nil
	}
	text := renderMilestone(notice, link)
	mile := milestoneMarker(notice)
	return n.retry(ctx, func(ctx context.Context) error {
		tok, err := n.token(ctx, repo)
		if err != nil {
			return err
		}
		return n.github.UpsertIssueComment(ctx, tok, repo, number, mile, text)
	})
}

func (n *Notifier) syncCheck(ctx context.Context, thread *Thread, key string, notice Notice, repo, link string) error {
	summary := renderStatus(notice, link)
	if thread.StatusMsgRef == "" {
		if notice.HeadSHA == "" {
			return nil
		}
		cr := checkRun(notice, summary, link)
		sha := notice.HeadSHA
		var id int64
		if err := n.retry(ctx, func(ctx context.Context) error {
			tok, err := n.token(ctx, repo)
			if err != nil {
				return err
			}
			id, err = n.github.CreateCheckRun(ctx, tok, repo, sha, cr)
			return err
		}); err != nil {
			return err
		}
		thread.StatusMsgRef = strconv.FormatInt(id, 10)
		if thread.Channel == "" {
			thread.Channel = repo
		}
		if err := n.save(ctx, *thread); err != nil {
			return err
		}
		n.markSent(slotKey(key, "check"))
		return nil
	}
	id, err := strconv.ParseInt(thread.StatusMsgRef, 10, 64)
	if err != nil {
		return fmt.Errorf("outbound: check run id: %w", err)
	}
	return n.edit(ctx, slotKey(key, "check"), func(ctx context.Context) error {
		tok, tokErr := n.token(ctx, repo)
		if tokErr != nil {
			return tokErr
		}
		return n.github.UpdateCheckRun(ctx, tok, repo, id, checkRun(notice, renderStatus(notice, link), link))
	})
}

func (n *Notifier) token(ctx context.Context, repo string) (github.Token, error) {
	id, err := n.github.InstallationByRepo(ctx, repo)
	if err != nil {
		return github.Token{}, err
	}
	return n.github.InstallationToken(ctx, id, []string{repo}, outboundPerms())
}

func outboundPerms() map[string]string {
	return map[string]string{
		"checks":   "write",
		"issues":   "write",
		"metadata": "read",
	}
}

func formatGitHubChannel(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

func parseGitHubChannel(channel string) (string, int, bool) {
	i := strings.LastIndex(channel, "#")
	if i <= 0 {
		return "", 0, false
	}
	number, err := strconv.Atoi(channel[i+1:])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return channel[:i], number, true
}

func (n *Notifier) deliverLinear(ctx context.Context, notice Notice) error {
	if n.linear == nil || notice.IssueID == "" {
		return nil
	}
	key := itemKey(ProviderLinear, notice.WorkItemID)
	unlock := n.lock(key)
	defer unlock()

	thread, err := n.load(ctx, notice.WorkItemID, ProviderLinear)
	if err != nil {
		return err
	}
	link := n.link(notice.RunID)
	issueID := notice.IssueID
	if thread.Channel != "" {
		issueID = thread.Channel
	}
	if thread.StatusMsgRef == "" {
		var id string
		text := renderStatus(notice, link)
		if err := n.retry(ctx, func(ctx context.Context) error {
			var createErr error
			id, createErr = n.linear.CreateComment(ctx, issueID, text)
			return createErr
		}); err != nil {
			return err
		}
		thread.Channel = issueID
		thread.RootRef = id
		thread.StatusMsgRef = id
		if err := n.save(ctx, thread); err != nil {
			return err
		}
		n.markSent(slotKey(key, "status"))
	} else {
		commentID := thread.StatusMsgRef
		if err := n.edit(ctx, slotKey(key, "status"), func(ctx context.Context) error {
			return n.linear.UpdateComment(ctx, commentID, renderStatus(notice, link))
		}); err != nil {
			return err
		}
	}
	if !milestone(notice.Kind) {
		return nil
	}
	text := renderMilestone(notice, link)
	_, err = n.linearCreate(ctx, issueID, text)
	return err
}

func (n *Notifier) linearCreate(ctx context.Context, issueID, text string) (string, error) {
	var id string
	err := n.retry(ctx, func(ctx context.Context) error {
		var createErr error
		id, createErr = n.linear.CreateComment(ctx, issueID, text)
		return createErr
	})
	return id, err
}

func (n *Notifier) markSent(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	slot := n.slots[key]
	if slot == nil {
		slot = &editSlot{}
		n.slots[key] = slot
	}
	slot.last = n.now()
}

func (n *Notifier) edit(ctx context.Context, key string, send func(context.Context) error) error {
	n.mu.Lock()
	slot := n.slots[key]
	if slot == nil {
		slot = &editSlot{}
		n.slots[key] = slot
	}
	now := n.now()
	if slot.last.IsZero() || now.Sub(slot.last) >= coalesceWindow {
		slot.last = now
		slot.pending = nil
		n.mu.Unlock()
		return n.retry(ctx, send)
	}
	slot.pending = send
	if slot.waiting {
		n.mu.Unlock()
		return nil
	}
	wait := coalesceWindow - now.Sub(slot.last)
	if wait <= 0 {
		slot.last = now
		slot.pending = nil
		n.mu.Unlock()
		return n.retry(ctx, send)
	}
	slot.waiting = true
	n.waitingFlushes++
	n.mu.Unlock()
	go n.flush(key, wait)
	return nil
}

func (n *Notifier) flush(key string, wait time.Duration) {
	defer n.finishFlush()
	if err := n.sleep(n.rootCtx, wait); err != nil {
		n.mu.Lock()
		if slot := n.slots[key]; slot != nil {
			slot.waiting = false
		}
		n.mu.Unlock()
		return
	}
	unlock := n.lock(itemKeyOf(key))
	defer unlock()
	n.mu.Lock()
	slot := n.slots[key]
	if slot == nil || slot.pending == nil {
		if slot != nil {
			slot.waiting = false
		}
		n.mu.Unlock()
		return
	}
	send := slot.pending
	slot.pending = nil
	slot.waiting = false
	slot.last = n.now()
	n.mu.Unlock()
	if err := n.retry(n.rootCtx, send); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		n.log.Error("outbound edit failed", "error", redact(err))
	}
}

func (n *Notifier) finishFlush() {
	n.mu.Lock()
	n.waitingFlushes--
	if n.waitingFlushes == 0 {
		n.idle.Broadcast()
	}
	n.mu.Unlock()
}
