package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Watch streams a run's events, rendering each transcript line to out.
// fromSeq, when non-nil, is sent as from_seq (events with seq greater than
// that value). After a dropped connection Watch reconnects with
// Last-Event-ID set to the last dispatched id and from_seq set to that seq.
// The returned code is 0 succeeded, 1 failed, 2 cancelled.
func (c *Client) Watch(ctx context.Context, runID string, fromSeq *int64, out io.Writer) (int, error) {
	if err := c.requireKey(); err != nil {
		return 1, err
	}
	if out == nil {
		out = io.Discard
	}
	var (
		lastID  string
		seq     int64
		haveSeq bool
		seen    = map[string]struct{}{}
		attempt int
	)
	if fromSeq != nil {
		seq = *fromSeq
		haveSeq = true
	}
	for {
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		q := url.Values{}
		if haveSeq {
			q.Set("from_seq", strconv.FormatInt(seq, 10))
		}
		endpoint, err := c.endpoint("/api/v1/agent/runs/"+url.PathEscape(runID)+"/events", q)
		if err != nil {
			return 1, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 1, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := c.http().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 1, ctx.Err()
			}
			attempt++
			if err := c.sleep(ctx, attempt); err != nil {
				return 1, err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				return 1, readErr
			}
			return 1, problemError(resp.StatusCode, raw)
		}
		code, done, err := readEvents(resp.Body, out, &lastID, &seq, &haveSeq, seen)
		_ = resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return 1, ctx.Err()
			}
			return 1, err
		}
		if done {
			return code, nil
		}
		attempt++
		if err := c.sleep(ctx, attempt); err != nil {
			return 1, err
		}
	}
}

func readEvents(body io.Reader, out io.Writer, lastID *string, seq *int64, haveSeq *bool, seen map[string]struct{}) (int, bool, error) {
	r := newSSEReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, false, nil
			}
			return 0, false, err
		}
		if ev.ID != "" {
			if _, ok := seen[ev.ID]; ok {
				continue
			}
			seen[ev.ID] = struct{}{}
			*lastID = ev.ID
			n, convErr := strconv.ParseInt(ev.ID, 10, 64)
			if convErr == nil {
				*seq = n
				*haveSeq = true
			}
		}
		if line, ok := RenderLine(ev); ok {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return 0, false, err
			}
		}
		code, terminal, err := terminalExit(ev)
		if err != nil {
			return 0, false, err
		}
		if terminal {
			return code, true, nil
		}
	}
}

func terminalExit(ev Event) (int, bool, error) {
	if ev.Type != "terminal" && ev.Type != "run.terminal" {
		return 0, false, nil
	}
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
		return 0, true, fmt.Errorf("terminal event: %w", err)
	}
	switch p.Status {
	case "succeeded":
		return 0, true, nil
	case "cancelled":
		return 2, true, nil
	default:
		// failed, and harness statuses such as model_unavailable and
		// config_invalid. The status line is already on stdout.
		return 1, true, nil
	}
}
