package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Watch streams a run's events, rendering each transcript line to out.
// fromSeq, when non-nil, is sent as from_seq (events with seq greater than
// that value). After a dropped connection Watch reconnects with
// Last-Event-ID set to the last dispatched id and from_seq set to that seq.
// A harness terminal is rendered and is not the end of the watch. The
// returned code is taken from control-plane run.terminal: 0 succeeded,
// 1 failed, 2 cancelled. A read error still returns (1, err) without
// reconnecting. Idle silence is the exception: it reconnects from the cursor.
func (c *Client) Watch(ctx context.Context, runID string, fromSeq *int64, out io.Writer) (int, error) {
	if err := c.requireKey(); err != nil {
		return 1, err
	}
	if out == nil {
		out = io.Discard
	}
	cur := &eventCursor{seen: map[string]struct{}{}}
	if fromSeq != nil {
		cur.seq = *fromSeq
		cur.haveSeq = true
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		resp, err := c.openRunEvents(ctx, runID, cur)
		if err != nil {
			var transport *streamTransportError
			if errors.As(err, &transport) {
				if ctx.Err() != nil {
					return 1, ctx.Err()
				}
				attempt++
				if err := c.sleep(ctx, attempt); err != nil {
					return 1, err
				}
				continue
			}
			return 1, err
		}
		code, done, err := readEvents(resp.Body, out, cur)
		_ = resp.Body.Close()
		if errors.Is(err, errStreamIdle) {
			if ctx.Err() != nil {
				return 1, ctx.Err()
			}
			if err := c.notifyIdle(ctx, runID); err != nil {
				if errors.Is(err, ErrStreamSettled) {
					return 0, nil
				}
				if ctx.Err() != nil {
					return 1, ctx.Err()
				}
				return 1, err
			}
			attempt++
			if err := c.sleep(ctx, attempt); err != nil {
				return 1, err
			}
			continue
		}
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

func readEvents(body io.Reader, out io.Writer, cur *eventCursor) (int, bool, error) {
	r := newSSEReader(body)
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, errStreamIdle) {
				return 0, false, err
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, false, nil
			}
			return 0, false, err
		}
		if !cur.accept(ev) {
			continue
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
	// The harness event type "terminal" is rendered above and is not the
	// end of the watch. factoryd flushes it before the run decision.
	if ev.Type != "run.terminal" {
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
		// failed, and control-plane statuses such as model_unavailable
		// and config_invalid. The status line is already on stdout.
		return 1, true, nil
	}
}
