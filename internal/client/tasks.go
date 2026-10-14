package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTaskPoll = 500 * time.Millisecond

// TaskRequest is the POST /api/v1/factory/{uid}/tasks body.
// SourceRef is the work item's source_ref column; the CLI flag is --ref.
type TaskRequest struct {
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Source    string `json:"source,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
}

// TaskFilter selects work items. Empty fields are omitted from the query.
type TaskFilter struct {
	Stage  string
	Status string
}

// WorkItem is a control-plane work item.
// Runs is an optional embedded child list. Watch also reads
// GET /api/v1/factory/{uid}/runs and streams rows with a matching work_item_id.
type WorkItem struct {
	ID             string `json:"id"`
	FactoryID      string `json:"factory_id,omitempty"`
	Source         string `json:"source,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	Title          string `json:"title,omitempty"`
	Body           string `json:"body,omitempty"`
	Stage          string `json:"stage,omitempty"`
	Status         string `json:"status,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	RevisionCount  int    `json:"revision_count,omitempty"`
	PendingGate    string `json:"pending_gate,omitempty"`
	Runs           []Run  `json:"runs,omitempty"`
}

// Approval is one gate decision record.
type Approval struct {
	ID         string `json:"id"`
	WorkItemID string `json:"work_item_id,omitempty"`
	Gate       string `json:"gate,omitempty"`
	Decision   string `json:"decision,omitempty"`
	Note       string `json:"note,omitempty"`
	DecidedBy  string `json:"decided_by,omitempty"`
}

type decideApprovalBody struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

// CreateTask creates a work item and returns it. The API key is a bearer token
// and is never copied into the body.
func (c *Client) CreateTask(ctx context.Context, factory string, req TaskRequest) (WorkItem, error) {
	if err := requireFactory(factory); err != nil {
		return WorkItem{}, err
	}
	var item WorkItem
	err := c.doJSON(ctx, http.MethodPost, factoryPath(factory, "tasks"), req, &item, http.StatusCreated)
	return item, err
}

// GetTask fetches one work item.
func (c *Client) GetTask(ctx context.Context, factory, id string) (WorkItem, error) {
	if err := requireFactory(factory); err != nil {
		return WorkItem{}, err
	}
	if err := requireID(id, "task id"); err != nil {
		return WorkItem{}, err
	}
	var item WorkItem
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "tasks", id), nil, &item, http.StatusOK)
	return item, err
}

// ListTasks lists work items for a factory.
func (c *Client) ListTasks(ctx context.Context, factory string, f TaskFilter) ([]WorkItem, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	q := url.Values{}
	if f.Stage != "" {
		q.Set("stage", f.Stage)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	var items []WorkItem
	err := c.doJSONQuery(ctx, http.MethodGet, factoryPath(factory, "tasks"), q, nil, &items, http.StatusOK)
	return items, err
}

// CancelTask requests cancellation and returns the work item.
func (c *Client) CancelTask(ctx context.Context, factory, id string) (WorkItem, error) {
	if err := requireFactory(factory); err != nil {
		return WorkItem{}, err
	}
	if err := requireID(id, "task id"); err != nil {
		return WorkItem{}, err
	}
	var item WorkItem
	path := factoryPath(factory, "tasks", id) + "/cancel"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &item, http.StatusOK)
	return item, err
}

// ListApprovals lists the factory's approval inbox.
func (c *Client) ListApprovals(ctx context.Context, factory string) ([]Approval, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	var items []Approval
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "approvals"), nil, &items, http.StatusOK)
	return items, err
}

// DecideApproval sends {decision, note} to POST /api/v1/approvals/{id}:decide.
func (c *Client) DecideApproval(ctx context.Context, id, decision, note string) (Approval, error) {
	if err := requireID(id, "approval id"); err != nil {
		return Approval{}, err
	}
	var item Approval
	path := "/api/v1/approvals/" + url.PathEscape(id) + ":decide"
	body := decideApprovalBody{Decision: decision, Note: note}
	err := c.doJSON(ctx, http.MethodPost, path, body, &item, http.StatusOK)
	return item, err
}

// WatchTask polls a work item until it is complete or cancelled, writing each
// stage change and each child run's transcript. The code is 0 for complete
// and 2 for cancelled.
func (c *Client) WatchTask(ctx context.Context, factory, taskID string, out io.Writer) (int, error) {
	if out == nil {
		out = io.Discard
	}
	seen := map[string]struct{}{}
	var last string
	for {
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		item, err := c.GetTask(ctx, factory, taskID)
		if err != nil {
			return 1, err
		}
		line := stageLine(item)
		if line != last {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return 1, err
			}
			last = line
		}
		runs, err := c.taskRuns(ctx, factory, taskID, item.Runs)
		if err != nil {
			return 1, err
		}
		for _, run := range runs {
			if _, ok := seen[run.ID]; ok {
				continue
			}
			seen[run.ID] = struct{}{}
			pw := &prefixWriter{prefix: "run=" + run.ID + " ", w: out}
			if _, err := c.Watch(ctx, run.ID, nil, pw); err != nil {
				return 1, err
			}
		}
		switch item.Status {
		case "complete":
			return 0, nil
		case "cancelled":
			return 2, nil
		}
		if err := sleepFor(ctx, c.taskPoll()); err != nil {
			return 1, err
		}
	}
}

// taskRuns unions an embedded runs array with factory runs for this task.
// Embedded rows are kept even when they omit work_item_id. Listed rows
// match on work_item_id. The same run id is returned once.
func (c *Client) taskRuns(ctx context.Context, factory, taskID string, embedded []Run) ([]Run, error) {
	listed, err := c.factoryRuns(ctx, factory)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(embedded)+len(listed))
	seen := map[string]struct{}{}
	add := func(run Run) {
		if run.ID == "" {
			return
		}
		if _, ok := seen[run.ID]; ok {
			return
		}
		seen[run.ID] = struct{}{}
		out = append(out, run)
	}
	for _, run := range embedded {
		add(run)
	}
	for _, run := range listed {
		if run.WorkItemID != taskID {
			continue
		}
		add(run)
	}
	return out, nil
}

func (c *Client) factoryRuns(ctx context.Context, factory string) ([]Run, error) {
	var runs []Run
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "runs"), nil, &runs, http.StatusOK)
	return runs, err
}

func stageLine(item WorkItem) string {
	line := fmt.Sprintf("stage=%s status=%s", item.Stage, item.Status)
	if item.PendingGate != "" {
		line += " gate=" + item.PendingGate
	}
	return line
}

func (c *Client) taskPoll() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return defaultTaskPoll
}

func sleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func requireFactory(factory string) error {
	if strings.TrimSpace(factory) == "" {
		return errors.New("factory is required")
	}
	return nil
}

func requireID(id, what string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New(what + " is required")
	}
	return nil
}

func factoryPath(factory string, parts ...string) string {
	var b strings.Builder
	b.WriteString("/api/v1/factory/")
	b.WriteString(url.PathEscape(factory))
	for _, part := range parts {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(part))
	}
	return b.String()
}

// prefixWriter writes each complete line with a prefix. Watch emits one
// transcript line per Write, including the trailing newline.
type prefixWriter struct {
	prefix string
	w      io.Writer
	buf    bytes.Buffer
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	_, _ = p.buf.Write(b)
	for {
		data := p.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), data[:i+1]...)
		p.buf.Next(i + 1)
		if _, err := io.WriteString(p.w, p.prefix); err != nil {
			return 0, err
		}
		if _, err := p.w.Write(line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
