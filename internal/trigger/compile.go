package trigger

import (
	"fmt"
	"strings"

	"github.com/polds/factory/internal/definition"
)

// Resolver resolves alias filter values to canonical ids at apply time.
type Resolver = definition.AliasResolver

// Matcher reports whether one normalized event satisfies a compiled trigger.
// Implementations are pure.
type Matcher interface {
	Match(e Event) bool
}

type compiled struct {
	provider string
	event    string
	clauses  []definition.FilterClause
	payload  map[string]any
}

// Compile validates t and returns a matcher. Bounds and alias exclusivity
// are errors. A nil resolver does not warn about unresolved aliases; pass
// CompileWith at apply time for that warning. The matcher is nil when the
// diagnostics include an error.
func Compile(t definition.Trigger) (Matcher, []definition.Diagnostic) {
	return CompileWith(t, nil)
}

// CompileWith is Compile with an alias resolver. r may be nil.
func CompileWith(t definition.Trigger, r Resolver) (Matcher, []definition.Diagnostic) {
	prog := definition.CheckTrigger(t, r)
	if !prog.OK {
		return nil, prog.Diags
	}
	return compiled{
		provider: t.Provider,
		event:    t.Event,
		clauses:  prog.Clauses,
		payload:  prog.Payload,
	}, prog.Diags
}

func (m compiled) Match(e Event) bool {
	if e.Provider != m.provider || e.Event != m.event {
		return false
	}
	for _, clause := range m.clauses {
		if !matchClause(e, clause) {
			return false
		}
	}
	if m.payload != nil && !matchPayload(m.payload, e.Raw) {
		return false
	}
	return true
}

func matchClause(e Event, c definition.FilterClause) bool {
	switch c.Kind {
	case definition.FilterGlob:
		paths, ok := valuesFor(e, c.Key)
		if !ok {
			return !c.HasIn
		}
		return matchPaths(c, paths)
	case definition.FilterKeyword:
		hay, ok := keywordHay(e)
		if !ok {
			return !c.HasIn
		}
		return matchKeyword(c, hay)
	case definition.FilterExact:
		vals, ok := valuesFor(e, c.Key)
		if !ok {
			return !c.HasIn
		}
		return matchSet(c, vals)
	default:
		panic(fmt.Sprintf("trigger: unhandled filter kind %d", c.Kind))
	}
}

func matchSet(c definition.FilterClause, vals []string) bool {
	if c.HasIn && !anyEqual(vals, c.In) {
		return false
	}
	if c.HasNotIn && anyEqual(vals, c.NotIn) {
		return false
	}
	return true
}

func matchKeyword(c definition.FilterClause, hay string) bool {
	if c.HasIn && !anyContains(hay, c.In) {
		return false
	}
	if c.HasNotIn && anyContains(hay, c.NotIn) {
		return false
	}
	return true
}

func matchPaths(c definition.FilterClause, paths []string) bool {
	if c.HasIn && !anyGlob(paths, c.In) {
		return false
	}
	if c.HasNotIn && anyGlob(paths, c.NotIn) {
		return false
	}
	return true
}

func anyEqual(vals, set []string) bool {
	for _, v := range vals {
		for _, s := range set {
			if v == s {
				return true
			}
		}
	}
	return false
}

func anyContains(hay string, keys []string) bool {
	for _, k := range keys {
		if k != "" && strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

func anyGlob(paths, globs []string) bool {
	for _, path := range paths {
		for _, g := range globs {
			if MatchGlob(g, path) {
				return true
			}
		}
	}
	return false
}
