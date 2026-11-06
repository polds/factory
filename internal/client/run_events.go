package client

import (
	"context"
	"errors"
	"io"
)

// errRunStreamDone ends StreamRunEvents after factoryd's run.terminal.
// The harness event type "terminal" is not the end of the stream: factoryd
// flushes it before it decides the run, and the authoritative status arrives
// later as run.terminal. Stopping on the harness event drops that status.
var errRunStreamDone = errors.New("run stream done")

// StreamRunEvents reads GET /api/v1/agent/runs/{runId}/events until the
// context ends or factoryd's run.terminal is delivered. A dropped connection
// reconnects from the last event id, including past a harness terminal.
func (c *Client) StreamRunEvents(ctx context.Context, runID string, yield func(Event) error) error {
	if err := c.requireKey(); err != nil {
		return err
	}
	if yield == nil {
		yield = func(Event) error { return nil }
	}
	cur := &eventCursor{seen: map[string]struct{}{}}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.openRunEvents(ctx, runID, cur)
		if err != nil {
			var transport *streamTransportError
			if errors.As(err, &transport) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				attempt++
				if err := c.sleep(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			return err
		}
		err = readRunEvents(resp.Body, yield, cur)
		_ = resp.Body.Close()
		if errors.Is(err, errRunStreamDone) {
			return nil
		}
		if errors.Is(err, errStreamIdle) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := c.notifyIdle(ctx, runID); err != nil {
				if errors.Is(err, ErrStreamSettled) {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			attempt++
			if sleepErr := c.sleep(ctx, attempt); sleepErr != nil {
				return sleepErr
			}
			continue
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

func readRunEvents(body io.Reader, yield func(Event) error, cur *eventCursor) error {
	r := newSSEReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, errStreamIdle) {
				return err
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if !cur.accept(ev) {
			continue
		}
		if err := yield(ev); err != nil {
			return err
		}
		if ev.Type == "run.terminal" {
			return errRunStreamDone
		}
	}
}
