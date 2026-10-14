// Package protocol is the worker WebSocket wire format from docs/04 §1.
// Liveness is a WebSocket ping/pong control frame, not a JSON message.
package protocol

import (
	"encoding/json"
	"fmt"
)

const (
	TypeTaskAssignment   = "task_assignment"
	TypeTaskClaimed      = "task_claimed"
	TypeTaskCompleted    = "task_completed"
	TypeTaskFailed       = "task_failed"
	TypeTaskRejected     = "task_rejected"
	TypeTaskCancellation = "task_cancellation"
)

// Envelope is the {type, data} wrapper on every JSON message.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Marshal encodes payload as a JSON envelope. Data is embedded as an object,
// not a string.
func Marshal(typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", typ, err)
	}
	raw, err := json.Marshal(Envelope{Type: typ, Data: data})
	if err != nil {
		return nil, fmt.Errorf("marshal %s envelope: %w", typ, err)
	}
	return raw, nil
}
