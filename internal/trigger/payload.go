package trigger

import (
	"bytes"
	"encoding/json"

	"github.com/polds/factory/internal/definition"
)

const maxJSONDepth = 64

type jkind int

const (
	jMissing jkind = iota
	jNull
	jBool
	jNum
	jStr
	jArr
	jObj
)

type jval struct {
	kind jkind
	b    bool
	n    float64
	s    string
	arr  []jval
	obj  map[string]jval
}

type leaf struct {
	hasIn    bool
	in       []any
	hasNotIn bool
	notIn    []any
	exists   *bool
}

func matchPayload(filter map[string]any, raw json.RawMessage) bool {
	root, ok := decodeJSON(raw)
	if !ok {
		root = jval{kind: jMissing}
	}
	return matchNode(filter, root)
}

func matchNode(filter map[string]any, node jval) bool {
	if node.kind == jArr {
		for _, el := range node.arr {
			if matchNode(filter, el) {
				return true
			}
		}
		return false
	}
	for key, child := range filter {
		childNode := jval{kind: jMissing}
		present := false
		if node.kind == jObj {
			if v, ok := node.obj[key]; ok {
				childNode = v
				present = true
			}
		}
		if !matchChild(child, childNode, present) {
			return false
		}
	}
	return true
}

func matchChild(filter any, node jval, present bool) bool {
	if items, ok := asAnySlice(filter); ok {
		return matchLeaf(leaf{hasIn: true, in: items}, node, present)
	}
	m, ok := filter.(map[string]any)
	if !ok {
		return false
	}
	if definition.IsPayloadMatcher(m) {
		return matchLeaf(leafFromMap(m), node, present)
	}
	return matchNode(m, node)
}

func leafFromMap(m map[string]any) leaf {
	var out leaf
	if items, ok := asAnySlice(m["in"]); ok {
		out.hasIn = true
		out.in = items
	}
	if items, ok := asAnySlice(m["not_in"]); ok {
		out.hasNotIn = true
		out.notIn = items
	}
	if b, ok := m["exists"].(bool); ok {
		out.exists = &b
	}
	return out
}

func matchLeaf(l leaf, node jval, present bool) bool {
	if l.exists != nil && *l.exists != present {
		return false
	}
	if !present {
		return !l.hasIn
	}
	vals := scalarVals(node)
	if l.hasIn && !anyScalar(l.in, vals) {
		return false
	}
	if l.hasNotIn && anyScalar(l.notIn, vals) {
		return false
	}
	return true
}

func scalarVals(node jval) []jval {
	switch node.kind {
	case jArr:
		var out []jval
		for _, el := range node.arr {
			out = append(out, scalarVals(el)...)
		}
		return out
	case jBool, jNum, jStr:
		return []jval{node}
	default:
		return nil
	}
}

func anyScalar(want []any, vals []jval) bool {
	for _, v := range vals {
		got, ok := jvalAny(v)
		if !ok {
			continue
		}
		for _, w := range want {
			if scalarEqual(w, got) {
				return true
			}
		}
	}
	return false
}

func scalarEqual(a, b any) bool {
	ak, aok := definition.ScalarKey(a)
	bk, bok := definition.ScalarKey(b)
	return aok && bok && ak == bk
}

func jvalAny(n jval) (any, bool) {
	switch n.kind {
	case jBool:
		return n.b, true
	case jNum:
		return n.n, true
	case jStr:
		return n.s, true
	default:
		return nil, false
	}
}

func asAnySlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func decodeJSON(raw json.RawMessage) (jval, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return jval{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return jval{}, false
	}
	return box(v, 0), true
}

func box(v any, depth int) jval {
	if depth > maxJSONDepth {
		return jval{kind: jNull}
	}
	switch n := v.(type) {
	case nil:
		return jval{kind: jNull}
	case bool:
		return jval{kind: jBool, b: n}
	case string:
		return jval{kind: jStr, s: n}
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return jval{kind: jStr, s: n.String()}
		}
		return jval{kind: jNum, n: f}
	case float64:
		return jval{kind: jNum, n: n}
	case []any:
		arr := make([]jval, len(n))
		for i, el := range n {
			arr[i] = box(el, depth+1)
		}
		return jval{kind: jArr, arr: arr}
	case map[string]any:
		obj := make(map[string]jval, len(n))
		for k, el := range n {
			obj[k] = box(el, depth+1)
		}
		return jval{kind: jObj, obj: obj}
	default:
		return jval{kind: jNull}
	}
}
