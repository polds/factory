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
