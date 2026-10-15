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
	body, err := c.checkBody(cr, true, headSHA)
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
	body, err := c.checkBody(cr, false, "")
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

func (c *Client) checkBody(cr CheckRun, create bool, headSHA string) (checkBody, error) {
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
	stamp := c.now().UTC().Format(time.RFC3339)
	switch cr.Status {
	case CheckQueued:
	case CheckInProgress:
		body.StartedAt = stamp
	case CheckCompleted:
		body.StartedAt = stamp
		body.CompletedAt = stamp
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
