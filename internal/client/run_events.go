package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// errRunStreamDone ends StreamRunEvents after a terminal event.
var errRunStreamDone = errors.New("run stream done")

// StreamRunEvents reads GET /api/v1/agent/runs/{runId}/events until the
// context ends or a terminal event is delivered. A dropped connection
// reconnects from the last event id.
func (c *Client) StreamRunEvents(ctx context.Context, runID string, yield func(Event) error) error {
	if err := c.requireKey(); err != nil {
		return err
	}
	if yield == nil {
		yield = func(Event) error { return nil }
	}
	var (
		lastID  string
		seq     int64
		haveSeq bool
		seen    = map[string]struct{}{}
		attempt int
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{}
		if haveSeq {
			q.Set("from_seq", strconv.FormatInt(seq, 10))
		}
		endpoint, err := c.endpoint("/api/v1/agent/runs/"+url.PathEscape(runID)+"/events", q)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := c.http().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			attempt++
			if err := c.sleep(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				return readErr
			}
			return problemError(resp.StatusCode, raw)
		}
		err = readRunEvents(resp.Body, yield, &lastID, &seq, &haveSeq, seen)
		_ = resp.Body.Close()
		if errors.Is(err, errRunStreamDone) {
			return nil
		}
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		attempt++
		if sleepErr := c.sleep(ctx, attempt); sleepErr != nil {
			return sleepErr
		}
	}
}

func readRunEvents(body io.Reader, yield func(Event) error, lastID *string, seq *int64, haveSeq *bool, seen map[string]struct{}) error {
	r := newSSEReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
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
		if err := yield(ev); err != nil {
			return err
		}
		if ev.Type == "terminal" || ev.Type == "run.terminal" {
			return errRunStreamDone
		}
	}
}
