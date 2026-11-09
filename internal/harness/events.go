package harness

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// pullURLRe matches an https pull request URL. The harness copies matches
// from tool output onto the tool result so factoryd can put them on
// run.terminal. Query strings are not part of the match. Trailing
// punctuation is stripped in PullURLs.
var pullURLRe = regexp.MustCompile(`https://[^\s"'<>]+/pull/[0-9]+`)

// pullURLTrail is punctuation a sentence can glue onto a copied URL.
const pullURLTrail = ".,;:!?)]}>\"'"

// EventEvidenceTest is the redacted, capped stdout and stderr of one
// evidence command. tool.result stays a digest and does not use this type.
const EventEvidenceTest = "evidence.test"

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
	// Steer is set on run.capabilities. SteerID identifies an applied
	// interrupt or follow-up. Text on those events is already redacted.
	Steer   bool   `json:"steer,omitempty"`
	SteerID string `json:"steer_id,omitempty"`
	// PRURLs are pull request URLs found in tool output. Empty stays omitted
	// so events that have no URL keep their previous JSON shape.
	PRURLs []string `json:"prUrls,omitempty"`
}

// PullURLs returns the distinct https pull request URLs in text, in order.
func PullURLs(text string) []string {
	found := pullURLRe.FindAllString(text, -1)
	if len(found) == 0 {
		return nil
	}
	out := make([]string, 0, len(found))
	seen := map[string]struct{}{}
	for _, raw := range found {
		raw = strings.TrimRight(raw, pullURLTrail)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	return out
}

// Log writes events with strictly increasing timestamps and at most one terminal.
// Emit is safe for the steer poller to call alongside the agent loop.
type Log struct {
	mu       sync.Mutex
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
	l.mu.Lock()
	defer l.mu.Unlock()
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

func (l *Log) Closed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terminal
}
