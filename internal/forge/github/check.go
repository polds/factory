package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CheckStatus is a GitHub check run status.
type CheckStatus string

const (
	CheckQueued     CheckStatus = "queued"
	CheckInProgress CheckStatus = "in_progress"
	CheckCompleted  CheckStatus = "completed"
)

// CheckRun is the create/update body for one check run.
// An empty Name uses "factory". Completed requires Conclusion.
// DetailsURL is the factory run link.
type CheckRun struct {
	Name       string
	Status     CheckStatus
	Conclusion string
	Summary    string
	DetailsURL string
}

type checkBody struct {
	Name        string   `json:"name,omitempty"`
	HeadSHA     string   `json:"head_sha,omitempty"`
	Status      string   `json:"status"`
	Conclusion  string   `json:"conclusion,omitempty"`
	DetailsURL  string   `json:"details_url,omitempty"`
	StartedAt   string   `json:"started_at,omitempty"`
	CompletedAt string   `json:"completed_at,omitempty"`
	Output      checkOut `json:"output"`
}

type checkOut struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CreateCheckRun opens a check run on headSHA and returns its id.
func (c *Client) CreateCheckRun(ctx context.Context, tok Token, repo, headSHA string, cr CheckRun) (int64, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(headSHA) == "" {
		return 0, errors.New("github: head sha is required")
	}
	now := c.now()
	body, err := c.checkBody(cr, true, headSHA, 0, now)
	if err != nil {
		return 0, err
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/check-runs"
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, path, tok.Value(), body, &out, "check_run", "checks"); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, errors.New("github: check run id missing")
	}
	if cr.Status == CheckInProgress {
		c.noteStart(out.ID, now)
	}
	c.log.Info("github check run created", "repo", owner+"/"+name, "id", out.ID, "status", string(cr.Status))
	return out.ID, nil
}

// UpdateCheckRun moves an existing check run to cr.Status.
func (c *Client) UpdateCheckRun(ctx context.Context, tok Token, repo string, id int64, cr CheckRun) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("github: check run id is required")
	}
	body, err := c.checkBody(cr, false, "", id, c.now())
	if err != nil {
		return err
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/check-runs/" + strconv.FormatInt(id, 10)
	if err := c.do(ctx, http.MethodPatch, path, tok.Value(), body, nil, "check_run", "checks"); err != nil {
		return err
	}
	c.log.Info("github check run updated", "repo", owner+"/"+name, "id", id, "status", string(cr.Status))
	return nil
}

func (c *Client) checkBody(cr CheckRun, create bool, headSHA string, id int64, now time.Time) (checkBody, error) {
	if err := cr.Status.valid(); err != nil {
		return checkBody{}, err
	}
	if cr.Status == CheckCompleted && strings.TrimSpace(cr.Conclusion) == "" {
		return checkBody{}, errors.New("github: completed check run requires a conclusion")
	}
	if cr.Status != CheckCompleted && strings.TrimSpace(cr.Conclusion) != "" {
		return checkBody{}, errors.New("github: conclusion is only valid when the check run is completed")
	}
	name := strings.TrimSpace(cr.Name)
	if name == "" {
		name = defaultCheckName
	}
	body := checkBody{
		Status:     string(cr.Status),
		DetailsURL: cr.DetailsURL,
		Output: checkOut{
			Title:   name,
			Summary: cr.Summary,
		},
	}
	switch cr.Status {
	case CheckQueued:
	case CheckInProgress:
		body.StartedAt = c.noteStart(id, now).UTC().Format(time.RFC3339)
	case CheckCompleted:
		if started, ok := c.takeStart(id); ok {
			body.StartedAt = started.UTC().Format(time.RFC3339)
		}
		body.CompletedAt = now.UTC().Format(time.RFC3339)
		body.Conclusion = cr.Conclusion
	default:
		return checkBody{}, fmt.Errorf("github: unsupported check status %q", string(neverStatus(cr.Status)))
	}
	if create {
		body.Name = name
		body.HeadSHA = headSHA
	}
	return body, nil
}

func (s CheckStatus) valid() error {
	switch s {
	case CheckQueued, CheckInProgress, CheckCompleted:
		return nil
	default:
		return fmt.Errorf("github: unsupported check status %q", string(neverStatus(s)))
	}
}

func neverStatus(s CheckStatus) CheckStatus { return s }

// noteStart records the first in_progress time for a check run.
// id 0 means the create response has not assigned an id yet.
func (c *Client) noteStart(id int64, started time.Time) time.Time {
	if id <= 0 {
		return started
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.checkStarted[id]; ok {
		return prev
	}
	c.checkStarted[id] = started
	return started
}

func (c *Client) takeStart(id int64) (time.Time, bool) {
	if id <= 0 {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	started, ok := c.checkStarted[id]
	delete(c.checkStarted, id)
	return started, ok
}
