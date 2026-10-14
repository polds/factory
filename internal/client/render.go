package client

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// RenderLine formats one transcript line. Unknown event types are skipped so
// fields outside the watch contract are not printed.
func RenderLine(ev Event) (string, bool) {
	switch ev.Type {
	case "message.delta":
		var p struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			return "", false
		}
		return fmt.Sprintf("message.delta role=%s text=%s", p.Role, p.Text), true
	case "tool.call":
		var p struct {
			Tool          string `json:"tool"`
			RedactedInput string `json:"redacted_input"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			return "", false
		}
		return fmt.Sprintf("tool.call tool=%s redacted_input=%s", p.Tool, p.RedactedInput), true
	case "tool.result":
		var p struct {
			Tool string `json:"tool"`
			OK   bool   `json:"ok"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			return "", false
		}
		return fmt.Sprintf("tool.result tool=%s ok=%s", p.Tool, strconv.FormatBool(p.OK)), true
	case "usage":
		var p struct {
			ModelID      string `json:"model_id"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
			CostCents    int    `json:"cost_cents"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			return "", false
		}
		return fmt.Sprintf("usage model_id=%s input_tokens=%d output_tokens=%d cost_cents=%d",
			p.ModelID, p.InputTokens, p.OutputTokens, p.CostCents), true
	case "terminal", "run.terminal":
		var p struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			return "", false
		}
		if p.Summary == "" {
			return fmt.Sprintf("terminal status=%s", p.Status), true
		}
		return fmt.Sprintf("terminal status=%s summary=%s", p.Status, p.Summary), true
	default:
		return "", false
	}
}
