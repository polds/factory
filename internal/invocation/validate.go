package invocation

import (
	"encoding/json"
	"fmt"
	"strings"
)

var allowedIntents = map[string]struct{}{
	"new": {}, "continue": {}, "wake": {},
}

var allowedPriorities = map[string]struct{}{
	"interactive": {}, "batch": {},
}

// ValidateRequest rejects closed-schema violations before authz.
func ValidateRequest(raw map[string]any) error {
	allowed := map[string]struct{}{
		"definitionRevision": {}, "profile": {}, "target": {}, "inputs": {},
		"sourceOccurrenceKey": {}, "correlation": {}, "intent": {}, "priority": {},
	}
	for k := range raw {
		if _, ok := allowed[k]; !ok {
			return Reject{Code: CodeInputInvalid, Status: 400}
		}
	}
	if _, ok := raw["principal"]; ok {
		return Reject{Code: CodeInputInvalid, Status: 400}
	}
	intent, _ := raw["intent"].(string)
	if _, ok := allowedIntents[intent]; !ok {
		return Reject{Code: CodeInputInvalid, Status: 400}
	}
	priority, _ := raw["priority"].(string)
	if _, ok := allowedPriorities[priority]; !ok {
		return Reject{Code: CodeInputInvalid, Status: 400}
	}
	key, _ := raw["sourceOccurrenceKey"].(string)
	if strings.TrimSpace(key) == "" {
		return Reject{Code: CodeInputInvalid, Status: 400}
	}
	if prof, ok := raw["profile"].(map[string]any); ok {
		if err := validateProfile(prof); err != nil {
			return err
		}
	}
	for k := range raw {
		if k == "budgetCeilingCents" || strings.HasPrefix(k, "grant") {
			return Reject{Code: CodeInputInvalid, Status: 400}
		}
	}
	return nil
}

func validateProfile(prof map[string]any) error {
	for k := range prof {
		if k != "name" {
			return Reject{Code: CodeProfileRejected, Status: 403}
		}
	}
	return nil
}

// DecodeRequest maps a validated JSON object into Request.
func DecodeRequest(raw map[string]any) (Request, error) {
	if err := ValidateRequest(raw); err != nil {
		return Request{}, err
	}
	var req Request
	req.Intent = raw["intent"].(string)
	req.Priority = raw["priority"].(string)
	req.SourceOccurrenceKey = raw["sourceOccurrenceKey"].(string)
	if rev, ok := raw["definitionRevision"].(string); ok {
		req.DefinitionRevision = rev
	}
	if prof, ok := raw["profile"].(map[string]any); ok {
		if name, ok := prof["name"].(string); ok {
			req.Profile.Name = name
		}
	}
	if tgt, ok := raw["target"].(map[string]any); ok {
		req.Target = &Target{}
		if repo, ok := tgt["repository"].(string); ok {
			req.Target.Repository = repo
		}
		if ref, ok := tgt["ref"].(string); ok {
			req.Target.Ref = ref
		}
		if pr, ok := tgt["pullRequest"].(float64); ok {
			req.Target.PullRequest = int(pr)
		}
	}
	if inputs, ok := raw["inputs"].(map[string]any); ok {
		req.Inputs = inputs
		if v, ok := inputs["executeTarget"].(bool); ok && v {
			req.ExecuteTargetGrant = true
		}
	}
	if corr, ok := raw["correlation"].(map[string]any); ok {
		req.Correlation = &Correlation{}
		if v, ok := corr["conversationId"].(string); ok {
			req.Correlation.ConversationID = v
		}
		if v, ok := corr["replyTo"].(string); ok {
			req.Correlation.ReplyTo = v
		}
		if v, ok := corr["threadRef"].(string); ok {
			req.Correlation.ThreadRef = v
		}
	}
	return req, nil
}

// ProfileInjectionFields reports injected profile keys for witnesses.
func ProfileInjectionFields(raw map[string]any) []string {
	prof, ok := raw["profile"].(map[string]any)
	if !ok {
		return nil
	}
	var injected []string
	for k := range prof {
		if k != "name" {
			injected = append(injected, k)
		}
	}
	return injected
}

// RawMap decodes JSON bytes to a map for validation.
func RawMap(body []byte) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, Reject{Code: CodeInputInvalid, Status: 400}
	}
	if raw == nil {
		return nil, Reject{Code: CodeInputInvalid, Status: 400}
	}
	return raw, nil
}

// ScheduleOccurrenceKey formats schedule:{id}:slot:{dueSlot}.
func ScheduleOccurrenceKey(scheduleID, dueSlot string) string {
	return fmt.Sprintf("schedule:%s:slot:%s", scheduleID, dueSlot)
}
