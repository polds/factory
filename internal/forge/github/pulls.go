package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CreatePullRequest opens a pull request and returns its number and URL.
// The call needs pull_requests permission. The error text does not contain
// the installation token.
func (c *Client) CreatePullRequest(ctx context.Context, tok Token, repo, head, base, title, body string) (int, string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(head) == "" || strings.TrimSpace(base) == "" {
		return 0, "", errors.New("github: pull request head and base are required")
	}
	if strings.TrimSpace(title) == "" {
		return 0, "", errors.New("github: pull request title is required")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(name))
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	in := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{Title: title, Head: head, Base: base, Body: body}
	if err := c.do(ctx, http.MethodPost, path, tok.Value(), in, &out, "pull_request", "pull_requests"); err != nil {
		return 0, "", err
	}
	if out.Number <= 0 || out.HTMLURL == "" {
		return 0, "", errors.New("github: pull request response is incomplete")
	}
	c.log.Info("github pull request created", "repo", owner+"/"+name, "number", out.Number)
	return out.Number, out.HTMLURL, nil
}

// RequestReviewers asks GitHub to request the logins on a pull request.
// The call needs pull_requests permission.
func (c *Client) RequestReviewers(ctx context.Context, tok Token, repo string, number int, logins []string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("github: pull request number is required")
	}
	if len(logins) == 0 {
		return errors.New("github: reviewers are required")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", url.PathEscape(owner), url.PathEscape(name), number)
	in := struct {
		Reviewers []string `json:"reviewers"`
	}{Reviewers: logins}
	if err := c.do(ctx, http.MethodPost, path, tok.Value(), in, nil, "request_reviewers", "pull_requests"); err != nil {
		return err
	}
	c.log.Info("github reviewers requested", "repo", owner+"/"+name, "number", number, "reviewers", len(logins))
	return nil
}

// AddLabels adds labels to a pull request through the issues labels route.
// The error names the issues permission, which is the route GitHub checks.
func (c *Client) AddLabels(ctx context.Context, tok Token, repo string, number int, labels []string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("github: pull request number is required")
	}
	if len(labels) == 0 {
		return errors.New("github: labels are required")
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", url.PathEscape(owner), url.PathEscape(name), number)
	in := struct {
		Labels []string `json:"labels"`
	}{Labels: labels}
	if err := c.do(ctx, http.MethodPost, path, tok.Value(), in, nil, "add_labels", "issues"); err != nil {
		return err
	}
	c.log.Info("github labels added", "repo", owner+"/"+name, "number", number, "labels", len(labels))
	return nil
}

// DraftPull is a pull request the publisher created or read back.
// HeadSHA is the commit GitHub reports for head.ref.
type DraftPull struct {
	Number  int
	URL     string
	Draft   bool
	HeadSHA string
	HeadRef string
	State   string
}

// CreateDraftPullRequest opens a pull request with draft set. The caller
// refuses a response that is not a draft or whose head SHA differs.
// CreatePullRequest is unchanged and does not send draft.
func (c *Client) CreateDraftPullRequest(ctx context.Context, tok Token, repo, head, base, title, body string) (DraftPull, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return DraftPull{}, err
	}
	if strings.TrimSpace(head) == "" || strings.TrimSpace(base) == "" {
		return DraftPull{}, errors.New("github: pull request head and base are required")
	}
	if strings.TrimSpace(title) == "" {
		return DraftPull{}, errors.New("github: pull request title is required")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(name))
	var out draftPullResponse
	in := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}{Title: title, Head: head, Base: base, Body: body, Draft: true}
	if err := c.do(ctx, http.MethodPost, path, tok.Value(), in, &out, "pull_request", "pull_requests"); err != nil {
		return DraftPull{}, err
	}
	got, err := out.pull()
	if err != nil {
		return DraftPull{}, err
	}
	c.log.Info("github draft pull request created", "repo", owner+"/"+name, "number", got.Number, "draft", got.Draft)
	return got, nil
}

// GetPullRequest reads one pull request. The publisher uses it to confirm
// the create response.
func (c *Client) GetPullRequest(ctx context.Context, tok Token, repo string, number int) (DraftPull, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return DraftPull{}, err
	}
	if number <= 0 {
		return DraftPull{}, errors.New("github: pull request number is required")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), number)
	var out draftPullResponse
	if err := c.do(ctx, http.MethodGet, path, tok.Value(), nil, &out, "get_pull", "pull_requests"); err != nil {
		return DraftPull{}, err
	}
	return out.pull()
}

// ListPullsByHeadPrefix scans every page of state=all and returns pulls
// whose head.ref has prefix. GET /pulls?head= is an exact filter, so the
// prefix is matched on head.ref and not on refs/heads/. A non-200 from any
// page, including 403 and 404, is returned and is not an empty list.
func (c *Client) ListPullsByHeadPrefix(ctx context.Context, tok Token, repo, prefix string) ([]DraftPull, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if prefix == "" || strings.HasPrefix(prefix, "refs/") {
		return nil, errors.New("github: pull head prefix")
	}
	const pageSize = 100
	const maxPages = 50
	var matched []DraftPull
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf(
			"/repos/%s/%s/pulls?state=all&per_page=%d&page=%d",
			url.PathEscape(owner), url.PathEscape(name), pageSize, page,
		)
		var batch []draftPullResponse
		if err := c.do(ctx, http.MethodGet, path, tok.Value(), nil, &batch, "list_pulls", "pull_requests"); err != nil {
			return nil, err
		}
		for _, item := range batch {
			got, err := item.pull()
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(got.HeadRef, prefix) {
				matched = append(matched, got)
			}
		}
		if len(batch) < pageSize {
			return matched, nil
		}
	}
	return nil, errors.New("github: pull list did not end")
}

type draftPullResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (r draftPullResponse) pull() (DraftPull, error) {
	if r.Number <= 0 || r.HTMLURL == "" || r.Head.Ref == "" {
		return DraftPull{}, errors.New("github: pull request response is incomplete")
	}
	return DraftPull{
		Number: r.Number, URL: r.HTMLURL, Draft: r.Draft, State: r.State,
		HeadSHA: r.Head.SHA, HeadRef: r.Head.Ref,
	}, nil
}
