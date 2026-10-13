package harness

import (
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Event is one NDJSON record from Harness Contract v1.
type Event struct {
	TS            string `json:"ts"`
	Type          string `json:"type"`
	Turn          int    `json:"turn,omitempty"`
	ModelID       string `json:"model_id,omitempty"`
	Role          string `json:"role,omitempty"`
	Text          string `json:"text,omitempty"`
	Tool          string `json:"tool,omitempty"`
	RedactedInput string `json:"redacted_input,omitempty"`
	InputDigest   string `json:"input_digest,omitempty"`
	OK            *bool  `json:"ok,omitempty"`
	DurationMS    *int64 `json:"duration_ms,omitempty"`
	OutputDigest  string `json:"output_digest,omitempty"`
	SkillPath     string `json:"skill_path,omitempty"`
	Path          string `json:"path,omitempty"`
	Additions     int    `json:"additions,omitempty"`
	Deletions     int    `json:"deletions,omitempty"`
	InputTokens   int    `json:"input_tokens,omitempty"`
	OutputTokens  int    `json:"output_tokens,omitempty"`
	CostCents     int    `json:"cost_cents,omitempty"`
	Gate          string `json:"gate,omitempty"`
	Status        string `json:"status,omitempty"`
	Summary       string `json:"summary,omitempty"`
}

// Log writes events with strictly increasing timestamps and at most one terminal.
type Log struct {
	w        io.Writer
	now      func() time.Time
	last     time.Time
	terminal bool
}

func NewLog(w io.Writer, now func() time.Time) *Log {
	if now == nil {
		now = time.Now
	}
	return &Log{w: w, now: now}
}

func (l *Log) Emit(e Event) error {
	if e.Type == "terminal" {
		if l.terminal {
			return errors.New("second terminal event")
		}
		l.terminal = true
	}
	ts := l.now().UTC()
	if !ts.After(l.last) {
		ts = l.last.Add(time.Nanosecond)
	}
	l.last = ts
	e.TS = ts.Format(time.RFC3339Nano)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = l.w.Write(append(b, '\n'))
	return err
}

func (l *Log) Closed() bool { return l.terminal }
