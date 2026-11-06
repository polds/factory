package harness

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Attempt is one Claude Code call. A call id is counted once, including failures.
type Attempt struct {
	ID           string
	Kind         string
	Status       string
	Text         string
	Tool         string
	ToolOutput   string
	HTTPStatus   int
	InputTokens  int
	OutputTokens int
}

// SessionResult is a mapped CLI stream. NegotiatedSteer is always false here.
type SessionResult struct {
	Attempts        []Attempt
	SessionID       string
	ResumeOf        string
	NegotiatedSteer bool
}

// ErrMalformed means the stream contained a line that was not JSON.
var ErrMalformed = errors.New("harness: malformed claude stream")

type claudeLine struct {
	Type      string       `json:"type"`
	Text      string       `json:"text"`
	Tool      string       `json:"tool"`
	Output    string       `json:"output"`
	OK        *bool        `json:"ok"`
	Error     string       `json:"error"`
	CallID    string       `json:"call_id"`
	Status    int          `json:"status"`
	SessionID string       `json:"session_id"`
	ResumeOf  string       `json:"resume_of"`
	Partial   bool         `json:"partial"`
	Usage     *claudeUsage `json:"usage"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// MapClaudeStream maps one NDJSON stream. Duplicate call ids do not create
// a second attempt. A usage line attaches to the earlier call.
func MapClaudeStream(r io.Reader) (SessionResult, error) {
	var result SessionResult
	seen := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var malformed error
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row claudeLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			result.Attempts = append(result.Attempts, Attempt{ID: "malformed", Kind: "error", Status: "malformed"})
			malformed = ErrMalformed
			break
		}
		applyClaudeLine(&result, seen, row)
	}
	if err := sc.Err(); err != nil {
		return result, err
	}
	if malformed != nil {
		return result, malformed
	}
	return result, nil
}

func applyClaudeLine(result *SessionResult, seen map[string]int, row claudeLine) {
	if row.ResumeOf != "" {
		result.ResumeOf = row.ResumeOf
	}
	if row.SessionID != "" {
		result.SessionID = row.SessionID
	}
	id := row.CallID
	if id == "" {
		id = row.Type
	}
	if row.Type == "usage" {
		if idx, ok := seen[id]; ok {
			if row.Usage != nil {
				result.Attempts[idx].InputTokens = row.Usage.InputTokens
				result.Attempts[idx].OutputTokens = row.Usage.OutputTokens
			}
			return
		}
	}
	if _, ok := seen[id]; ok {
		return
	}
	attempt := Attempt{ID: id, Kind: row.Type}
	switch row.Type {
	case "assistant":
		attempt.Kind = "text"
		attempt.Text = row.Text
		attempt.Status = "ok"
		if row.Partial {
			attempt.Status = "partial"
		}
	case "tool_result":
		attempt.Kind = "tool"
		attempt.Tool = row.Tool
		attempt.ToolOutput = row.Output
		attempt.Status = "ok"
		if row.OK != nil && !*row.OK {
			attempt.Status = "tool_error"
			attempt.Text = row.Error
		}
	case "refusal":
		attempt.Kind = "refusal"
		attempt.Status = "refusal"
		attempt.Text = row.Text
	case "error":
		attempt.Kind = "error"
		attempt.HTTPStatus = row.Status
		attempt.Status = errorStatus(row)
		attempt.Text = row.Error
	case "usage":
		attempt.Kind = "usage"
		attempt.Status = "ok"
		if row.Usage != nil {
			attempt.InputTokens = row.Usage.InputTokens
			attempt.OutputTokens = row.Usage.OutputTokens
		}
	case "resume":
		attempt.Kind = "resume"
		attempt.Status = "ok"
	default:
		attempt.Kind = "error"
		attempt.Status = "malformed"
	}
	seen[id] = len(result.Attempts)
	result.Attempts = append(result.Attempts, attempt)
}

func errorStatus(row claudeLine) string {
	switch row.Status {
	case 400:
		return "400"
	case 401:
		return "401"
	case 408:
		return "timeout"
	case 429:
		return "429"
	default:
		if row.Error == "timeout" {
			return "timeout"
		}
		return "error"
	}
}

// RedactAttempts replaces planted secrets in text fields.
func RedactAttempts(attempts []Attempt, needles []string) {
	for i := range attempts {
		attempts[i].Text = Redact(attempts[i].Text, needles)
		attempts[i].ToolOutput = Redact(attempts[i].ToolOutput, needles)
	}
}

// AccountedOnce reports that every attempt id appears once.
func (r SessionResult) AccountedOnce() bool {
	if len(r.Attempts) == 0 {
		return false
	}
	seen := map[string]struct{}{}
	for _, attempt := range r.Attempts {
		if attempt.ID == "" {
			return false
		}
		if _, ok := seen[attempt.ID]; ok {
			return false
		}
		seen[attempt.ID] = struct{}{}
	}
	return true
}
