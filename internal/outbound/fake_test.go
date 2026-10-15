package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

var (
	errWorkItem = errors.New("store: work item is required")
	errMissing  = errors.New("linear comment missing")
)

type memThreads struct {
	mu   sync.Mutex
	rows map[string]store.OutboundThread
}

func newMemThreads() *memThreads {
	return &memThreads{rows: map[string]store.OutboundThread{}}
}

func threadKey(workItemID, provider string) string {
	return workItemID + "\x00" + provider
}

func (m *memThreads) GetOutboundThread(_ context.Context, workItemID, provider string) (store.OutboundThread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[threadKey(workItemID, provider)]
	if !ok {
		return store.OutboundThread{}, store.ErrNotFound
	}
	return row, nil
}

func (m *memThreads) UpsertOutboundThread(_ context.Context, t store.OutboundThread) (store.OutboundThread, error) {
	if t.WorkItemID == "" {
		return store.OutboundThread{}, errWorkItem
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[threadKey(t.WorkItemID, t.Provider)] = t
	return t, nil
}

type fakeRefs struct {
	mu   sync.Mutex
	rows []store.ExternalRef
}

func (f *fakeRefs) BindExternalRef(_ context.Context, provider, externalRef, workItemID, conversationID string) (store.ExternalRef, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.Provider == provider && row.ExternalRef == externalRef {
			return row, false, nil
		}
	}
	row := store.ExternalRef{
		Provider:       provider,
		ExternalRef:    externalRef,
		WorkItemID:     workItemID,
		ConversationID: conversationID,
	}
	f.rows = append(f.rows, row)
	return row, true, nil
}

func (f *fakeRefs) snapshot() []store.ExternalRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ExternalRef, len(f.rows))
	copy(out, f.rows)
	return out
}

type slackMsg struct {
	channel string
	thread  string
	ts      string
	text    string
	update  bool
}

type fakeSlack struct {
	mu       sync.Mutex
	seq      int
	msgs     []slackMsg
	attempts int
	err      error
	updated  chan struct{}
}

func newFakeSlack() *fakeSlack {
	return &fakeSlack{updated: make(chan struct{}, 8)}
}

func (f *fakeSlack) Post(_ context.Context, channel, threadTS, text string, _ json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.err != nil {
		return "", f.err
	}
	f.seq++
	ts := "100." + pad3(f.seq)
	f.msgs = append(f.msgs, slackMsg{channel: channel, thread: threadTS, ts: ts, text: text})
	return ts, nil
}

func (f *fakeSlack) Update(_ context.Context, channel, ts, text string, _ json.RawMessage) error {
	f.mu.Lock()
	f.attempts++
	if f.err != nil {
		err := f.err
		f.mu.Unlock()
		return err
	}
	f.msgs = append(f.msgs, slackMsg{channel: channel, thread: ts, ts: ts, text: text, update: true})
	f.mu.Unlock()
	select {
	case f.updated <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSlack) snapshot() []slackMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]slackMsg, len(f.msgs))
	copy(out, f.msgs)
	return out
}

func pad3(n int) string {
	return fmt.Sprintf("%03d", n)
}

type ghComment struct {
	marker string
	bodies []string
}

type ghCheck struct {
	id         int64
	sha        string
	status     github.CheckStatus
	conclusion string
	summary    string
	details    string
	create     bool
}

type fakeGH struct {
	mu     sync.Mutex
	perms  map[string]string
	repos  []string
	mints  int
	checks []ghCheck
	notes  []ghComment
	err    error
}

func (f *fakeGH) InstallationByRepo(context.Context, string) (int64, error) {
	if err := f.fail(); err != nil {
		return 0, err
	}
	return 7, nil
}

func (f *fakeGH) InstallationToken(_ context.Context, _ int64, repos []string, perms map[string]string) (github.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return github.Token{}, f.err
	}
	f.mints++
	f.repos = append([]string{}, repos...)
	f.perms = map[string]string{}
	for k, v := range perms {
		f.perms[k] = v
	}
	return github.Token{}, nil
}

func (f *fakeGH) CreateCheckRun(_ context.Context, _ github.Token, _, headSHA string, cr github.CheckRun) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	id := int64(len(f.checks) + 1)
	f.checks = append(f.checks, ghCheck{
		id: id, sha: headSHA, status: cr.Status, conclusion: cr.Conclusion,
		summary: cr.Summary, details: cr.DetailsURL, create: true,
	})
	return id, nil
}

func (f *fakeGH) UpdateCheckRun(_ context.Context, _ github.Token, _ string, id int64, cr github.CheckRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.checks = append(f.checks, ghCheck{
		id: id, status: cr.Status, conclusion: cr.Conclusion,
		summary: cr.Summary, details: cr.DetailsURL,
	})
	return nil
}

func (f *fakeGH) UpsertIssueComment(_ context.Context, _ github.Token, _ string, _ int, marker, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for i := range f.notes {
		if f.notes[i].marker == marker {
			f.notes[i].bodies = append(f.notes[i].bodies, body)
			return nil
		}
	}
	f.notes = append(f.notes, ghComment{marker: marker, bodies: []string{body}})
	return nil
}

func (f *fakeGH) fail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeGH) snapshot() (perms map[string]string, repos []string, checks []ghCheck, notes []ghComment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	perms = map[string]string{}
	for k, v := range f.perms {
		perms[k] = v
	}
	repos = append([]string{}, f.repos...)
	checks = append([]ghCheck{}, f.checks...)
	for _, note := range f.notes {
		bodies := append([]string{}, note.bodies...)
		notes = append(notes, ghComment{marker: note.marker, bodies: bodies})
	}
	return perms, repos, checks, notes
}

type linNote struct {
	id    string
	issue string
	body  string
}

type fakeLinear struct {
	mu       sync.Mutex
	seq      int
	notes    []linNote
	updates  int
	attempts int
}

func (f *fakeLinear) CreateComment(_ context.Context, issueID, markdown string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	f.seq++
	id := "cmt_" + pad3(f.seq)
	f.notes = append(f.notes, linNote{id: id, issue: issueID, body: markdown})
	return id, nil
}

func (f *fakeLinear) UpdateComment(_ context.Context, commentID, markdown string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	f.updates++
	for i := range f.notes {
		if f.notes[i].id == commentID {
			f.notes[i].body = markdown
			return nil
		}
	}
	return errMissing
}

func (f *fakeLinear) snapshot() (notes []linNote, updates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	notes = append([]linNote{}, f.notes...)
	return notes, f.updates
}
