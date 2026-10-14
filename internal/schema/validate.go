package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidateJSON reports whether instance satisfies schema.
// schema must be a draft 2020-12 document this package can evaluate.
func ValidateJSON(schema, instance []byte) error {
	if err := CheckDraft(schema); err != nil {
		return err
	}
	sdoc, err := unmarshalJSON(schema)
	if err != nil {
		return err
	}
	idoc, err := unmarshalJSON(instance)
	if err != nil {
		return fmt.Errorf("instance is not JSON: %w", err)
	}
	return (&validator{root: sdoc}).apply(sdoc, idoc, "$")
}

type validator struct {
	root  any
	stack []string
}

func (v *validator) apply(schema, instance any, path string) error {
	switch s := schema.(type) {
	case bool:
		if s {
			return nil
		}
		return fmt.Errorf("%s: rejected", path)
	case map[string]any:
		if ref, ok := s["$ref"]; ok {
			if err := v.applyRef(ref, instance, path); err != nil {
				return err
			}
		}
		return v.keywords(s, instance, path)
	default:
		return fmt.Errorf("%s: schema must be an object or boolean", path)
	}
}

func (v *validator) applyRef(ref, instance any, path string) error {
	refStr, ok := ref.(string)
	if !ok {
		return fmt.Errorf("%s: $ref must be a string", path)
	}
	for _, seen := range v.stack {
		if seen == refStr {
			return fmt.Errorf("%s: cyclic $ref %s", path, refStr)
		}
	}
	target, err := lookupPointer(v.root, refStr)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	v.stack = append(v.stack, refStr)
	err = v.apply(target, instance, path)
	v.stack = v.stack[:len(v.stack)-1]
	return err
}

func (v *validator) keywords(s map[string]any, instance any, path string) error {
	for key := range s {
		if !appliedKeyword(key) {
			return fmt.Errorf("%s: unsupported keyword %q", path, key)
		}
	}
	if spec, ok := s["type"]; ok {
		if err := checkType(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["const"]; ok {
		if !equalValue(spec, instance) {
			return fmt.Errorf("%s: const mismatch", path)
		}
	}
	if spec, ok := s["enum"]; ok {
		if err := checkEnum(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["additionalProperties"]; ok {
		if err := v.checkAdditional(s["properties"], spec, instance, path); err != nil {
			return err
		}
	} else if spec, ok := s["properties"]; ok {
		if err := v.checkDefined(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["required"]; ok {
		if err := checkRequired(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["items"]; ok {
		if err := v.checkItems(spec, s["minItems"], s["maxItems"], instance, path); err != nil {
			return err
		}
	} else if _, ok := s["minItems"]; ok || hasKey(s, "maxItems") {
		if err := v.checkItems(nil, s["minItems"], s["maxItems"], instance, path); err != nil {
			return err
		}
	}
	if err := checkNumberBounds(s, instance, path); err != nil {
		return err
	}
	if err := checkStringBounds(s, instance, path); err != nil {
		return err
	}
	if spec, ok := s["allOf"]; ok {
		if err := v.checkAllOf(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["anyOf"]; ok {
		if err := v.checkAnyOf(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["oneOf"]; ok {
		if err := v.checkOneOf(spec, instance, path); err != nil {
			return err
		}
	}
	if spec, ok := s["not"]; ok {
		if v.matches(spec, instance) {
			return fmt.Errorf("%s: matched not", path)
		}
	}
	if spec, ok := s["if"]; ok {
		if v.matches(spec, instance) {
			if th, ok := s["then"]; ok {
				if err := v.apply(th, instance, path); err != nil {
					return err
				}
			}
		} else if el, ok := s["else"]; ok {
			if err := v.apply(el, instance, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func appliedKeyword(key string) bool {
	switch key {
	case "$schema", "$id", "$anchor", "$comment", "$vocabulary", "$defs", "$ref",
		"title", "description", "default", "examples", "deprecated", "readOnly", "writeOnly",
		"type", "const", "enum", "properties", "additionalProperties", "required",
		"items", "minItems", "maxItems", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
		"minLength", "maxLength", "pattern", "allOf", "anyOf", "oneOf", "not", "if", "then", "else":
		return true
	default:
		return false
	}
}

func (v *validator) matches(schema, instance any) bool {
	return v.apply(schema, instance, "$if") == nil
}

func (v *validator) checkDefined(spec, instance any, path string) error {
	obj, ok := instance.(map[string]any)
	if !ok {
		return nil
	}
	props, ok := spec.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: properties must be an object", path)
	}
	for key, sub := range props {
		if val, exists := obj[key]; exists {
			if err := v.apply(sub, val, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *validator) checkAdditional(propsSpec, addSpec, instance any, path string) error {
	obj, ok := instance.(map[string]any)
	if !ok {
		return nil
	}
	var props map[string]any
	if propsSpec != nil {
		var ok bool
		props, ok = propsSpec.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: properties must be an object", path)
		}
		for key, sub := range props {
			if val, exists := obj[key]; exists {
				if err := v.apply(sub, val, path+"."+key); err != nil {
					return err
				}
			}
		}
	}
	for key, val := range obj {
		if _, defined := props[key]; defined {
			continue
		}
		switch add := addSpec.(type) {
		case bool:
			if !add {
				return fmt.Errorf("%s: unknown property %q", path, key)
			}
		default:
			if err := v.apply(add, val, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRequired(spec, instance any, path string) error {
	obj, ok := instance.(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := spec.([]any)
	if !ok {
		return fmt.Errorf("%s: required must be an array", path)
	}
	for _, item := range arr {
		name, ok := item.(string)
		if !ok {
			return fmt.Errorf("%s: required entries must be strings", path)
		}
		if _, exists := obj[name]; !exists {
			return fmt.Errorf("%s: missing property %q", path, name)
		}
	}
	return nil
}

func (v *validator) checkItems(items, minSpec, maxSpec, instance any, path string) error {
	arr, ok := instance.([]any)
	if !ok {
		return nil
	}
	if items != nil {
		for i, item := range arr {
			if err := v.apply(items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	if minSpec != nil {
		min, ok := asFloat(minSpec)
		if !ok {
			return fmt.Errorf("%s: minItems must be a number", path)
		}
		if float64(len(arr)) < min {
			return fmt.Errorf("%s: fewer than %v items", path, minSpec)
		}
	}
	if maxSpec != nil {
		max, ok := asFloat(maxSpec)
		if !ok {
			return fmt.Errorf("%s: maxItems must be a number", path)
		}
		if float64(len(arr)) > max {
			return fmt.Errorf("%s: more than %v items", path, maxSpec)
		}
	}
	return nil
}

func checkNumberBounds(s map[string]any, instance any, path string) error {
	if _, ok := asFloat(instance); !ok {
		return nil
	}
	inst, _ := asFloat(instance)
	bounds := []struct {
		key       string
		exclusive bool
		less      bool
	}{
		{key: "minimum", less: true},
		{key: "exclusiveMinimum", exclusive: true, less: true},
		{key: "maximum", less: false},
		{key: "exclusiveMaximum", exclusive: true, less: false},
	}
	for _, bound := range bounds {
		spec, ok := s[bound.key]
		if !ok {
			continue
		}
		limit, ok := asFloat(spec)
		if !ok {
			return fmt.Errorf("%s: %s must be a number", path, bound.key)
		}
		if bound.less {
			if bound.exclusive && inst <= limit || !bound.exclusive && inst < limit {
				return fmt.Errorf("%s: fails %s", path, bound.key)
			}
			continue
		}
		if bound.exclusive && inst >= limit || !bound.exclusive && inst > limit {
			return fmt.Errorf("%s: fails %s", path, bound.key)
		}
	}
	return nil
}

func checkStringBounds(s map[string]any, instance any, path string) error {
	str, ok := instance.(string)
	if !ok {
		return nil
	}
	n := utf8.RuneCountInString(str)
	if spec, ok := s["minLength"]; ok {
		min, ok := asFloat(spec)
		if !ok {
			return fmt.Errorf("%s: minLength must be a number", path)
		}
		if float64(n) < min {
			return fmt.Errorf("%s: shorter than %v", path, spec)
		}
	}
	if spec, ok := s["maxLength"]; ok {
		max, ok := asFloat(spec)
		if !ok {
			return fmt.Errorf("%s: maxLength must be a number", path)
		}
		if float64(n) > max {
			return fmt.Errorf("%s: longer than %v", path, spec)
		}
	}
	if spec, ok := s["pattern"]; ok {
		pat, ok := spec.(string)
		if !ok {
			return fmt.Errorf("%s: pattern must be a string", path)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("%s: pattern: %w", path, err)
		}
		if !re.MatchString(str) {
			return fmt.Errorf("%s: does not match pattern", path)
		}
	}
	return nil
}

func (v *validator) checkAllOf(spec, instance any, path string) error {
	arr, err := schemaArray(spec, path, "allOf")
	if err != nil {
		return err
	}
	for _, sub := range arr {
		if err := v.apply(sub, instance, path); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) checkAnyOf(spec, instance any, path string) error {
	arr, err := schemaArray(spec, path, "anyOf")
	if err != nil {
		return err
	}
	for _, sub := range arr {
		if v.matches(sub, instance) {
			return nil
		}
	}
	return fmt.Errorf("%s: matched no anyOf alternative", path)
}

func (v *validator) checkOneOf(spec, instance any, path string) error {
	arr, err := schemaArray(spec, path, "oneOf")
	if err != nil {
		return err
	}
	matched := 0
	for _, sub := range arr {
		if v.matches(sub, instance) {
			matched++
		}
	}
	if matched != 1 {
		return fmt.Errorf("%s: want exactly one oneOf match, got %d", path, matched)
	}
	return nil
}

func schemaArray(spec any, path, keyword string) ([]any, error) {
	arr, ok := spec.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s must be an array", path, keyword)
	}
	return arr, nil
}

func checkType(spec, instance any, path string) error {
	names, err := typeNames(spec)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, name := range names {
		if !knownJSONType(name) {
			return fmt.Errorf("%s: unknown type %q", path, name)
		}
		if matchesType(name, instance) {
			return nil
		}
	}
	return fmt.Errorf("%s: does not match type %s", path, strings.Join(names, " or "))
}

func typeNames(spec any) ([]string, error) {
	switch t := spec.(type) {
	case string:
		return []string{t}, nil
	case []any:
		names := make([]string, 0, len(t))
		for _, item := range t {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("type array must contain strings")
			}
			names = append(names, name)
		}
		return names, nil
	default:
		return nil, fmt.Errorf("type must be a string or array")
	}
}

func matchesType(name string, instance any) bool {
	switch name {
	case "object":
		_, ok := instance.(map[string]any)
		return ok
	case "array":
		_, ok := instance.([]any)
		return ok
	case "string":
		_, ok := instance.(string)
		return ok
	case "boolean":
		_, ok := instance.(bool)
		return ok
	case "null":
		return instance == nil
	case "integer":
		return isInteger(instance)
	case "number":
		_, ok := asFloat(instance)
		return ok
	default:
		return false
	}
}

func checkEnum(spec, instance any, path string) error {
	arr, ok := spec.([]any)
	if !ok {
		return fmt.Errorf("%s: enum must be an array", path)
	}
	for _, item := range arr {
		if equalValue(item, instance) {
			return nil
		}
	}
	return fmt.Errorf("%s: value not in enum", path)
}

func equalValue(a, b any) bool {
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok || bok {
		return aok && bok && af == bf
	}
	if as, ok := a.(string); ok {
		bs, ok := b.(string)
		return ok && as == bs
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		return ok && ab == bb
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(ab) == string(bb)
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, false
		}
		return f, true
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return 0, false
		}
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func isInteger(v any) bool {
	switch n := v.(type) {
	case json.Number:
		s := string(n)
		if s == "" {
			return false
		}
		i := 0
		if s[0] == '-' {
			i++
		}
		if i >= len(s) {
			return false
		}
		for ; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	case int, int64:
		return true
	case float64:
		return !math.IsInf(n, 0) && !math.IsNaN(n) && n == math.Trunc(n)
	default:
		return false
	}
}

func lookupPointer(doc any, pointer string) (any, error) {
	if pointer == "#" {
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "#/") {
		return nil, fmt.Errorf("unsupported $ref %q", pointer)
	}
	cur := doc
	for _, raw := range strings.Split(pointer[2:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, fmt.Errorf("$ref %q: missing %q", pointer, token)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, fmt.Errorf("$ref %q: bad index %q", pointer, token)
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("$ref %q: cannot descend into %q", pointer, token)
		}
	}
	return cur, nil
}
