package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// UpsertIssueComment creates or updates the issue comment that contains marker.
// marker is the hidden HTML comment (<!-- factory:wi_… -->). A second call with
// the same marker updates that comment instead of posting another one.
func (c *Client) UpsertIssueComment(ctx context.Context, tok Token, repo string, number int, marker, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if number <= 0 {
		return errors.New("github: issue number is required")
	}
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return errors.New("github: comment marker is required")
	}
	text := composeComment(marker, body)
	existing, err := c.findComment(ctx, tok, owner, name, number, marker)
	if err != nil {
		return err
	}
	if existing == nil {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(name), number)
		if err := c.do(ctx, http.MethodPost, path, tok.Value(), map[string]string{"body": text}, nil, "issue_comment", "issues"); err != nil {
			return err
		}
		c.log.Info("github issue comment created", "repo", owner+"/"+name, "issue", number)
		return nil
	}
	if existing.Body == text {
		c.log.Debug("github issue comment unchanged", "repo", owner+"/"+name, "issue", number, "comment_id", existing.ID)
		return nil
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d", url.PathEscape(owner), url.PathEscape(name), existing.ID)
	if err := c.do(ctx, http.MethodPatch, path, tok.Value(), map[string]string{"body": text}, nil, "issue_comment", "issues"); err != nil {
		return err
	}
	c.log.Info("github issue comment updated", "repo", owner+"/"+name, "issue", number, "comment_id", existing.ID)
	return nil
}

func composeComment(marker, body string) string {
	if strings.Contains(body, marker) {
		return body
	}
	return marker + "\n" + body
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

func (c *Client) findComment(ctx context.Context, tok Token, owner, name string, number int, marker string) (*issueComment, error) {
	const perPage = 100
	const maxPages = 10
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d",
			url.PathEscape(owner), url.PathEscape(name), number, perPage, page)
		var comments []issueComment
		if err := c.do(ctx, http.MethodGet, path, tok.Value(), nil, &comments, "issue_comment", "issues"); err != nil {
			return nil, err
		}
		for i := range comments {
			if strings.Contains(comments[i].Body, marker) {
				found := comments[i]
				return &found, nil
			}
		}
		if len(comments) < perPage {
			return nil, nil
		}
	}
	return nil, errors.New("github: issue comment search exceeded 1000 comments")
}
