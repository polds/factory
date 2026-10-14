package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const draft202012 = "https://json-schema.org/draft/2020-12/schema"

// CheckDraft reports whether schema is a JSON Schema draft 2020-12 document.
// The root object must set $schema to the draft 2020-12 meta-schema, and
// every keyword must belong to that draft.
func CheckDraft(schema []byte) error {
	doc, err := unmarshalJSON(schema)
	if err != nil {
		return fmt.Errorf("schema is not JSON: %w", err)
	}
	if err := checkDraftSchema(doc, true); err != nil {
		return err
	}
	return nil
}

func unmarshalJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing JSON")
	}
	return doc, nil
}

func checkDraftSchema(schema any, root bool) error {
	switch s := schema.(type) {
	case bool:
		if root {
			return errors.New("root schema must be an object")
		}
		return nil
	case map[string]any:
		if root {
			raw, _ := s["$schema"].(string)
			if raw != draft202012 {
				return fmt.Errorf("$schema = %q, want %q", raw, draft202012)
			}
		}
		for key, val := range s {
			if err := checkKeyword(key, val); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		return nil
	default:
		return errors.New("schema must be an object or boolean")
	}
}

func checkKeyword(key string, val any) error {
	switch key {
	case "$schema":
		s, ok := val.(string)
		if !ok || s != draft202012 {
			return fmt.Errorf("must be %s", draft202012)
		}
		return nil
	case "$id", "$ref", "$anchor", "$dynamicRef", "$dynamicAnchor", "$comment",
		"title", "description", "format", "contentEncoding", "contentMediaType":
		if _, ok := val.(string); !ok {
			return errors.New("must be a string")
		}
		return nil
	case "pattern":
		s, ok := val.(string)
		if !ok {
			return errors.New("must be a string")
		}
		if _, err := regexp.Compile(s); err != nil {
			return err
		}
		return nil
	case "$vocabulary", "dependentSchemas", "$defs", "patternProperties":
		return checkSchemaMap(val)
	case "dependentRequired":
		m, ok := val.(map[string]any)
		if !ok {
			return errors.New("must be an object")
		}
		for _, item := range m {
			if err := checkStringArray(item); err != nil {
				return err
			}
		}
		return nil
	case "additionalProperties", "unevaluatedProperties", "unevaluatedItems",
		"items", "contains", "not", "if", "then", "else", "propertyNames", "contentSchema":
		return checkDraftSchema(val, false)
	case "properties":
		return checkSchemaMap(val)
	case "prefixItems", "allOf", "anyOf", "oneOf":
		return checkSchemaArray(val)
	case "required":
		return checkStringArray(val)
	case "type":
		return checkTypeKeyword(val)
	case "enum", "examples":
		if _, ok := val.([]any); !ok {
			return errors.New("must be an array")
		}
		return nil
	case "const", "default":
		return nil
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
		if _, ok := asFloat(val); !ok {
			return errors.New("must be a number")
		}
		return nil
	case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties",
		"minContains", "maxContains":
		return nonNegativeInteger(val)
	case "uniqueItems", "deprecated", "readOnly", "writeOnly":
		if _, ok := val.(bool); !ok {
			return errors.New("must be a boolean")
		}
		return nil
	default:
		return errors.New("unknown draft 2020-12 keyword")
	}
}

func checkSchemaMap(val any) error {
	m, ok := val.(map[string]any)
	if !ok {
		return errors.New("must be an object of schemas")
	}
	for name, item := range m {
		if err := checkDraftSchema(item, false); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func checkSchemaArray(val any) error {
	arr, ok := val.([]any)
	if !ok {
		return errors.New("must be an array of schemas")
	}
	for i, item := range arr {
		if err := checkDraftSchema(item, false); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

func checkStringArray(val any) error {
	arr, ok := val.([]any)
	if !ok {
		return errors.New("must be an array of strings")
	}
	for _, item := range arr {
		if _, ok := item.(string); !ok {
			return errors.New("must be an array of strings")
		}
	}
	return nil
}

func checkTypeKeyword(val any) error {
	switch t := val.(type) {
	case string:
		if !knownJSONType(t) {
			return fmt.Errorf("unknown type %q", t)
		}
		return nil
	case []any:
		if len(t) == 0 {
			return errors.New("type array is empty")
		}
		for _, item := range t {
			name, ok := item.(string)
			if !ok || !knownJSONType(name) {
				return errors.New("type array must contain JSON types")
			}
		}
		return nil
	default:
		return errors.New("must be a string or array of strings")
	}
}

func knownJSONType(name string) bool {
	switch name {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func nonNegativeInteger(val any) error {
	n, ok := asFloat(val)
	if !ok || n < 0 || n != float64(int64(n)) {
		return errors.New("must be a non-negative integer")
	}
	return nil
}
