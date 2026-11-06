package invocation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

func marshalCanonical(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf.Write(key)
			buf.WriteByte(':')
			val, err := marshalCanonical(t[k])
			if err != nil {
				return nil, err
			}
			buf.Write(val)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			val, err := marshalCanonical(item)
			if err != nil {
				return nil, err
			}
			buf.Write(val)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	default:
		return json.Marshal(v)
	}
}

func bodyMap(req Request) map[string]any {
	out := map[string]any{
		"intent":              req.Intent,
		"priority":            req.Priority,
		"sourceOccurrenceKey": req.SourceOccurrenceKey,
	}
	if req.DefinitionRevision != "" {
		out["definitionRevision"] = req.DefinitionRevision
	}
	if req.Profile.Name != "" {
		out["profile"] = map[string]any{"name": req.Profile.Name}
	}
	if req.Target != nil {
		tgt := map[string]any{"repository": req.Target.Repository}
		if req.Target.Ref != "" {
			tgt["ref"] = req.Target.Ref
		}
		if req.Target.PullRequest > 0 {
			tgt["pullRequest"] = req.Target.PullRequest
		}
		out["target"] = tgt
	}
	if len(req.Inputs) > 0 {
		out["inputs"] = req.Inputs
	}
	if req.Correlation != nil {
		corr := map[string]any{}
		if req.Correlation.ConversationID != "" {
			corr["conversationId"] = req.Correlation.ConversationID
		}
		if req.Correlation.ReplyTo != "" {
			corr["replyTo"] = req.Correlation.ReplyTo
		}
		if req.Correlation.ThreadRef != "" {
			corr["threadRef"] = req.Correlation.ThreadRef
		}
		if len(corr) > 0 {
			out["correlation"] = corr
		}
	}
	return out
}

// Digest computes the canonical SHA-256 hex digest for req.
func Digest(req Request) string {
	raw, err := marshalCanonical(bodyMap(req))
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CanonicalBody returns the canonical map for witnesses and tests.
func CanonicalBody(req Request) map[string]any {
	return bodyMap(req)
}
