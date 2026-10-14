package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/polds/factory/internal/events"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func (s *Server) finish(ctx context.Context, runID, execID, state, reason, summary string, exit *int, taskState *protocol.TaskState) {
	if execID == "" {
		execID = s.execForRun(runID)
	}
	if execID == "" {
		s.log.Error("terminal without execution", "run_id", runID)
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		s.log.Error("terminal run", "run_id", runID, "err", err)
		return
	}
	if run.CancelRequested || (taskState != nil && *taskState == protocol.TaskStateCancelled) {
		state = store.ExecCancelled
	}
	applied, err := s.store.FinishExecution(ctx, execID, store.Terminal{
		State:         state,
		FailureReason: reason,
		ExitCode:      exit,
	})
	if err != nil {
		s.log.Error("finish execution", "run_id", runID, "execution_id", execID, "err", err)
		return
	}
	s.stopGrace(runID)
	s.untrack(execID)
	if !applied {
		return
	}
	status, err := runStatus(state)
	if err != nil {
		s.log.Error("terminal state", "run_id", runID, "err", err)
		return
	}
	if err := s.appendServerEvent(ctx, runID, execID, serverEventBase, terminalPayload(s.now(), status, reason, summary, exit)); err != nil {
		s.log.Error("terminal event", "run_id", runID, "err", err)
	}
	s.archive(ctx, runID, execID)
	s.hub.Publish(runID)
	s.nudge()
}

func terminalPayload(now time.Time, status, reason, summary string, exit *int) []byte {
	payload := map[string]any{
		"type":   "run.terminal",
		"ts":     now.UTC().Format(time.RFC3339Nano),
		"status": status,
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if reason != "" {
		payload["failure_reason"] = reason
	}
	if exit != nil {
		payload["exit_code"] = *exit
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"type":"run.terminal","status":"` + status + `"}`)
	}
	return raw
}

func runStatus(state string) (string, error) {
	switch state {
	case store.ExecCompleted:
		return store.StatusSucceeded, nil
	case store.ExecFailed:
		return store.StatusFailed, nil
	case store.ExecCancelled:
		return store.StatusCancelled, nil
	default:
		return "", fmt.Errorf("server: terminal state %q", state)
	}
}

func (s *Server) appendServerEvent(ctx context.Context, runID, execID string, idx int, payload []byte) error {
	_, err := s.store.AppendEvents(ctx, runID, execID, idx, []json.RawMessage{payload})
	if err != nil {
		return err
	}
	s.hub.Publish(runID)
	return nil
}

func (s *Server) archive(ctx context.Context, runID, execID string) {
	if strings.TrimSpace(s.cfg.S3.Endpoint) == "" {
		return
	}
	evs, err := s.allEvents(ctx, runID)
	if err != nil {
		s.markUnavailable(ctx, runID, execID)
		return
	}
	var buf bytes.Buffer
	for _, ev := range evs {
		if ev.ExecutionID != execID {
			continue
		}
		buf.Write(ev.Payload)
		if !bytes.HasSuffix(ev.Payload, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	key := "runs/" + runID + "/" + execID + "/events.ndjson"
	// One Put. The client retries HTTP 5xx itself. A transport error returns
	// immediately and is a hard failure: the run stays terminal.
	if err := s.cfg.S3.Put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "application/x-ndjson"); err != nil {
		s.log.Error("archive events", "run_id", runID, "execution_id", execID, "err", err)
		s.markUnavailable(ctx, runID, execID)
	}
}

func (s *Server) markUnavailable(ctx context.Context, runID, execID string) {
	payload, err := json.Marshal(map[string]string{
		"type": "transcript_unavailable",
		"ts":   s.now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	if err := s.appendServerEvent(ctx, runID, execID, serverEventBase+1, payload); err != nil {
		s.log.Error("transcript_unavailable", "run_id", runID, "err", err)
	}
}

func (s *Server) allEvents(ctx context.Context, runID string) ([]store.Event, error) {
	var all []store.Event
	var from int64
	for {
		page, err := s.store.EventsFrom(ctx, runID, from, 500)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		from = page[len(page)-1].Seq
		if len(page) < 500 {
			return all, nil
		}
	}
}

// logSource is the events.Source adapter. It is a separate type because
// gateway.Sessions and events.Source both require a method named Terminal
// with different signatures, and a Go type can declare that name once.
type logSource struct {
	s *Server
}

func (src logSource) EventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]events.Event, error) {
	return src.s.eventsFrom(ctx, runID, fromSeq, limit)
}

func (src logSource) Terminal(ctx context.Context, runID string) (bool, error) {
	return src.s.runTerminal(ctx, runID)
}

func (s *Server) eventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]events.Event, error) {
	if limit <= 0 {
		limit = 256
	}
	rows, err := s.store.EventsFrom(ctx, runID, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	out := make([]events.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, events.Event{
			Seq:  row.Seq,
			Type: sseType(row.Type),
			Data: row.Payload,
		})
	}
	return out, nil
}

func (s *Server) runTerminal(ctx context.Context, runID string) (bool, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return isTerminalStatus(run.Status), nil
}

func isTerminalStatus(status string) bool {
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		return true
	default:
		return false
	}
}

func sseType(typ string) string {
	typ = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, typ)
	if typ == "" {
		return "message"
	}
	return typ
}

func (s *Server) failConfig(ctx context.Context, runID, workerID string, cause error) {
	s.log.Error("assignment config", "run_id", runID, "err", cause)
	sum := tokenHash("config-invalid")
	exec, err := s.store.StartExecution(ctx, runID, workerID, sum)
	if err != nil {
		s.log.Error("config execution", "run_id", runID, "err", err)
		if relErr := s.releaseClaim(ctx, runID); relErr != nil {
			s.log.Error("release claim", "run_id", runID, "err", relErr)
		}
		return
	}
	s.finish(ctx, runID, exec.ID, store.ExecFailed, "config_invalid", cause.Error(), nil, nil)
}
