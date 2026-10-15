package definition

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Bounds for trigger filters. Nesting and leaf counts apply to webhook
// payload filters. The value cap applies to every in/not_in set.
const (
	maxFilterNesting = 10
	maxFilterLeaves  = 64
	maxFilterValues  = 32
)

// FilterNotAuthorizationWarning is the validator text required by docs/07
// and docs/11. Filters decide when work starts. They are not an
// authorization boundary.
const FilterNotAuthorizationWarning = "filters decide when work starts, not what a running agent can reach; a filter is not an authorization boundary"

// FilterKind is how a clause compares event values.
type FilterKind int

const (
	// FilterExact is case-sensitive equality.
	FilterExact FilterKind = iota
	// FilterGlob is a doublestar path pattern.
	FilterGlob
	// FilterKeyword is a case-sensitive substring of the subject text.
	FilterKeyword
)

// FilterClause is one AND-term of a trigger filter after alias resolution.
type FilterClause struct {
	Key      string
	Kind     FilterKind
	In       []string
	NotIn    []string
	HasIn    bool
	HasNotIn bool
}

// FilterProgram is a validated trigger filter. OK is false when Diags
// contains an error. Clauses are alias-resolved; Payload is the webhook
// payload object when the trigger has one.
type FilterProgram struct {
	Diags   []Diagnostic
	Clauses []FilterClause
	Payload map[string]any
	OK      bool
}

// AliasResolver maps an alias name to its canonical id at apply time.
// provider is the trigger provider. aliasKey is the filter key that held
// the name (channels, teams, …). ok is false when the name is unknown.
type AliasResolver interface {
	Resolve(provider, aliasKey, name string) (id string, ok bool)
}

type filterProvider struct {
	keys    map[string]FilterKind
	aliases map[string]string // alias key → canonical key
}

// CheckTrigger validates one trigger filter. r may be nil; alias names are
// then left as written and no unresolved-alias warning is produced.
// Unresolved warnings are apply-time diagnostics, emitted only when r is set
// and does not know a name.
func CheckTrigger(t Trigger, r AliasResolver) FilterProgram {
	prog := FilterProgram{OK: true}
	if len(t.Filter) == 0 {
		return prog
	}
	spec, known := filterProviders[t.Provider]
	var diags []Diagnostic
	var leaves int
	for _, key := range sortedMapKeys(t.Filter) {
		val := t.Filter[key]
		if key == "payload" {
			if t.Provider != "webhook" {
				diags = append(diags, filterError("unknown filter key %q for provider %q", key, t.Provider))
				continue
			}
			more, payload, n := checkPayload(val)
			diags = append(diags, more...)
			leaves += n
			if !diagHasError(more) {
				prog.Payload = payload
			}
			continue
		}
		kind, ok := spec.keys[key]
		if !known || !ok {
			diags = append(diags, filterError("unknown filter key %q for provider %q", key, t.Provider))
			continue
		}
		canon := key
		if c, isAlias := spec.aliases[key]; isAlias {
			if _, also := t.Filter[c]; also {
				diags = append(diags, filterError("%s and %s are mutually exclusive", key, c))
				continue
			}
			canon = c
			val = resolveAlias(t.Provider, key, val, r, &diags)
		}
		if key == "webhook_ids" || canon == "webhook_ids" {
			clause, more := parseWebhookIDs(val)
			diags = append(diags, more...)
			if !diagHasError(more) {
				prog.Clauses = append(prog.Clauses, clause)
			}
			continue
		}
		clause, more := parseClause(canon, kind, val, false)
		diags = append(diags, more...)
		if !diagHasError(more) {
			prog.Clauses = append(prog.Clauses, clause)
		}
	}
	if leaves > maxFilterLeaves {
		diags = append(diags, filterError("payload filter has %d leaves; limit is %d", leaves, maxFilterLeaves))
	}
	prog.Diags = diags
	prog.OK = !diagHasError(diags)
	if !prog.OK {
		prog.Clauses = nil
		prog.Payload = nil
	}
	return prog
}

// CheckAutomations validates every automation trigger and reports overlap,
// webhook-id uniqueness, and the filter-is-not-authorization warning.
// File is automations/<name>/automation.md. r may be nil.
func CheckAutomations(autos map[string]Automation, r AliasResolver) []Diagnostic {
	names := make([]string, 0, len(autos))
	for name := range autos {
		names = append(names, name)
	}
	sort.Strings(names)

	var diags []Diagnostic
	type compiled struct {
		name     string
		enabled  bool
		provider string
		event    string
		prog     FilterProgram
	}
	var programs []compiled
	type webhookUse struct {
		name string
		id   string
	}
	var uses []webhookUse

	for _, name := range names {
		auto := autos[name]
		file := "automations/" + name + "/automation.md"
		hasFilter := false
		for _, tr := range auto.Triggers {
			if len(tr.Filter) > 0 {
				hasFilter = true
			}
			prog := CheckTrigger(tr, r)
			for _, d := range prog.Diags {
				if d.Line <= 0 {
					d.Line = 1
				}
				d.File = file
				diags = append(diags, d)
			}
			if !prog.OK {
				continue
			}
			for _, c := range prog.Clauses {
				if c.Key == "webhook_ids" && len(c.In) == 1 {
					uses = append(uses, webhookUse{name: name, id: c.In[0]})
				}
			}
			programs = append(programs, compiled{
				name: name, enabled: auto.Enabled,
				provider: tr.Provider, event: tr.Event, prog: prog,
			})
		}
		if hasFilter {
			diags = append(diags, Diagnostic{
				File: file, Line: 1, Severity: SeverityWarning,
				Message: FilterNotAuthorizationWarning,
			})
		}
	}

	sort.Slice(uses, func(i, j int) bool {
		if uses[i].id != uses[j].id {
			return uses[i].id < uses[j].id
		}
		return uses[i].name < uses[j].name
	})
	seenDup := map[string]bool{}
	for i := 1; i < len(uses); i++ {
		if uses[i].id != uses[i-1].id || seenDup[uses[i].id] {
			continue
		}
		seenDup[uses[i].id] = true
		diags = append(diags, Diagnostic{
			File: "automations/" + uses[i].name + "/automation.md",
			Line: 1, Severity: SeverityError,
			Message: fmt.Sprintf("webhook %q is used by more than one trigger", uses[i].id),
		})
	}

	for i := 0; i < len(programs); i++ {
		for j := i + 1; j < len(programs); j++ {
			a, b := programs[i], programs[j]
			if a.name == b.name || !a.enabled || !b.enabled {
				continue
			}
			if a.provider != b.provider || a.event != b.event {
				continue
			}
			if provablyDisjoint(a.prog, b.prog) {
				continue
			}
			left, right := a, b
			if right.name < left.name {
				left, right = right, left
			}
			diags = append(diags, Diagnostic{
				File: "automations/" + right.name + "/automation.md",
				Line: 1, Severity: SeverityWarning,
				Message: fmt.Sprintf("automation %q overlaps automation %q on %s/%s; filters are not provably disjoint", right.name, left.name, right.provider, right.event),
			})
		}
	}
	return diags
}

// IsPayloadMatcher reports whether v is an {in, not_in, exists} object
// rather than a nested payload object. An empty object is nested.
func IsPayloadMatcher(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return false
	}
	for k := range m {
		switch k {
		case "in", "not_in", "exists":
		default:
			return false
		}
	}
	return true
}

// ScalarKey is the equality key for a payload scalar. Strings, bools, and
// numbers do not compare equal to each other. The bool false is not the
// string "false".
func ScalarKey(v any) (string, bool) {
	switch n := v.(type) {
	case string:
		return "s:" + n, true
	case bool:
		if n {
			return "b:1", true
		}
		return "b:0", true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", false
		}
		return "n:" + strconv.FormatFloat(n, 'g', -1, 64), true
	case float32:
		return ScalarKey(float64(n))
	case int:
		return ScalarKey(float64(n))
	case int64:
		return ScalarKey(float64(n))
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return "", false
		}
		return ScalarKey(f)
	default:
		return "", false
	}
}

func checkPayload(v any) ([]Diagnostic, map[string]any, int) {
	m, ok := v.(map[string]any)
	if !ok {
		return []Diagnostic{filterError("payload filter must be an object")}, nil, 0
	}
	var diags []Diagnostic
	leaves := walkPayload(m, 1, "", &diags)
	if diagHasError(diags) {
		return diags, nil, leaves
	}
	return diags, m, leaves
}

func walkPayload(m map[string]any, depth int, path string, diags *[]Diagnostic) int {
	if depth > maxFilterNesting {
		*diags = append(*diags, filterError("payload filter nesting %d exceeds %d", depth, maxFilterNesting))
		return 0
	}
	leaves := 0
	for _, key := range sortedMapKeys(m) {
		child := m[key]
		p := key
		if path != "" {
			p = path + "." + key
		}
		if mixedOperator(child) {
			*diags = append(*diags, filterError("filter key %q mixes operators and nested fields", p))
			continue
		}
		if _, isList := asSlice(child); isList || IsPayloadMatcher(child) {
			leaves++
			_, more := parseClause(p, FilterExact, child, true)
			*diags = append(*diags, more...)
			continue
		}
		nested, ok := child.(map[string]any)
		if !ok {
			*diags = append(*diags, filterError("filter key %q must be a list or matcher", p))
			continue
		}
		leaves += walkPayload(nested, depth+1, p, diags)
	}
	return leaves
}

func mixedOperator(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	op, other := false, false
	for k := range m {
		switch k {
		case "in", "not_in", "exists":
			op = true
		default:
			other = true
		}
	}
	return op && other
}

func parseWebhookIDs(val any) (FilterClause, []Diagnostic) {
	clause, diags := parseClause("webhook_ids", FilterExact, val, false)
	if clause.HasNotIn {
		diags = append(diags, filterError("webhook_ids accepts in only"))
	}
	if !clause.HasIn || len(clause.In) != 1 {
		diags = append(diags, filterError("webhook_ids must list exactly one id"))
	}
	return clause, diags
}

func parseClause(key string, kind FilterKind, val any, allowExists bool) (FilterClause, []Diagnostic) {
	clause := FilterClause{Key: key, Kind: kind}
	if items, ok := asSlice(val); ok {
		in, diags := scalarList(key, items)
		clause.HasIn = true
		clause.In = in
		if len(items) > maxFilterValues {
			diags = append(diags, filterError("filter set for %q has %d values; limit is %d", key, len(items), maxFilterValues))
		}
		return clause, diags
	}
	m, ok := val.(map[string]any)
	if !ok {
		return clause, []Diagnostic{filterError("filter key %q must be a list or matcher", key)}
	}
	var diags []Diagnostic
	for _, op := range sortedMapKeys(m) {
		switch op {
		case "in", "not_in":
			items, ok := asSlice(m[op])
			if !ok {
				diags = append(diags, filterError("filter key %q operator %q must be a list", key, op))
				continue
			}
			vals, more := scalarList(key, items)
			diags = append(diags, more...)
			if len(items) > maxFilterValues {
				diags = append(diags, filterError("filter set for %q has %d values; limit is %d", key, len(items), maxFilterValues))
			}
			if op == "in" {
				clause.HasIn = true
				clause.In = vals
			} else {
				clause.HasNotIn = true
				clause.NotIn = vals
			}
		case "exists":
			if !allowExists {
				diags = append(diags, filterError("exists is only valid on webhook payload fields"))
				continue
			}
			if _, ok := m[op].(bool); !ok {
				diags = append(diags, filterError("payload filter exists must be a bool"))
			}
		default:
			diags = append(diags, filterError("unknown filter operator %q", op))
		}
	}
	return clause, diags
}

func resolveAlias(provider, aliasKey string, val any, r AliasResolver, diags *[]Diagnostic) any {
	if items, ok := asSlice(val); ok {
		return resolveNames(provider, aliasKey, items, r, diags)
	}
	m, ok := val.(map[string]any)
	if !ok {
		return val
	}
	out := make(map[string]any, len(m))
	for k, child := range m {
		out[k] = child
	}
	for _, op := range []string{"in", "not_in"} {
		items, ok := asSlice(out[op])
		if !ok {
			continue
		}
		out[op] = resolveNames(provider, aliasKey, items, r, diags)
	}
	return out
}

func resolveNames(provider, aliasKey string, items []any, r AliasResolver, diags *[]Diagnostic) []any {
	out := make([]any, len(items))
	for i, item := range items {
		name, ok := item.(string)
		if !ok || r == nil {
			out[i] = item
			continue
		}
		id, found := r.Resolve(provider, aliasKey, name)
		if !found {
			*diags = append(*diags, Diagnostic{
				Line: 1, Severity: SeverityWarning,
				Message: fmt.Sprintf("unresolved %s %s alias %q", provider, aliasKey, name),
			})
			out[i] = name
			continue
		}
		out[i] = id
	}
	return out
}

func scalarList(key string, items []any) ([]string, []Diagnostic) {
	var diags []Diagnostic
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := canonScalar(item)
		if !ok {
			diags = append(diags, filterError("filter key %q has a non-scalar value", key))
			continue
		}
		out = append(out, s)
	}
	return out, diags
}

func canonScalar(v any) (string, bool) {
	switch n := v.(type) {
	case string:
		return n, true
	case bool:
		if n {
			return "true", true
		}
		return "false", true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", false
		}
		if n == math.Trunc(n) && n >= math.MinInt64 && n <= math.MaxInt64 {
			return strconv.FormatInt(int64(n), 10), true
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	case int:
		return strconv.Itoa(n), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case json.Number:
		return n.String(), true
	default:
		return "", false
	}
}

func asSlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []string:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func provablyDisjoint(a, b FilterProgram) bool {
	if clausesDisjoint(a.Clauses, b.Clauses) {
		return true
	}
	if a.Payload != nil && b.Payload != nil && payloadDisjoint(a.Payload, b.Payload) {
		return true
	}
	return false
}

func clausesDisjoint(a, b []FilterClause) bool {
	bm := map[string]FilterClause{}
	for _, c := range b {
		bm[c.Key] = c
	}
	for _, ca := range a {
		cb, ok := bm[ca.Key]
		if !ok || ca.Kind != FilterExact || cb.Kind != FilterExact {
			continue
		}
		if finiteDisjoint(ca, cb) {
			return true
		}
	}
	return false
}

func finiteDisjoint(a, b FilterClause) bool {
	af, aFinite := finiteAccept(a)
	bf, bFinite := finiteAccept(b)
	switch {
	case aFinite && bFinite:
		return !intersects(af, bf)
	case aFinite && !b.HasIn && b.HasNotIn:
		return subset(af, b.NotIn)
	case bFinite && !a.HasIn && a.HasNotIn:
		return subset(bf, a.NotIn)
	default:
		return false
	}
}

func finiteAccept(c FilterClause) ([]string, bool) {
	if !c.HasIn {
		return nil, false
	}
	if !c.HasNotIn {
		return c.In, true
	}
	var out []string
	for _, v := range c.In {
		if !containsStr(c.NotIn, v) {
			out = append(out, v)
		}
	}
	return out, true
}

func payloadDisjoint(a, b map[string]any) bool {
	for key, av := range a {
		bv, ok := b[key]
		if !ok {
			continue
		}
		if ls, lok := leafSetOf(av); lok {
			rs, rok := leafSetOf(bv)
			if rok && leafDisjoint(ls, rs) {
				return true
			}
			continue
		}
		am, aok := av.(map[string]any)
		bm, bok := bv.(map[string]any)
		if aok && bok && !IsPayloadMatcher(av) && !IsPayloadMatcher(bv) && payloadDisjoint(am, bm) {
			return true
		}
	}
	return false
}

type atomSet struct {
	finite bool
	in     []string
	notIn  []string
	hasNot bool
}

func leafSetOf(v any) (atomSet, bool) {
	if items, ok := asSlice(v); ok {
		keys, ok := atomKeys(items)
		if !ok {
			return atomSet{}, false
		}
		return atomSet{finite: true, in: keys}, true
	}
	m, ok := v.(map[string]any)
	if !ok || !IsPayloadMatcher(m) {
		return atomSet{}, false
	}
	set := atomSet{}
	if raw, ok := m["in"]; ok {
		items, ok := asSlice(raw)
		if !ok {
			return atomSet{}, false
		}
		keys, ok := atomKeys(items)
		if !ok {
			return atomSet{}, false
		}
		set.in = keys
		set.finite = true
	}
	if raw, ok := m["not_in"]; ok {
		items, ok := asSlice(raw)
		if !ok {
			return atomSet{}, false
		}
		keys, ok := atomKeys(items)
		if !ok {
			return atomSet{}, false
		}
		set.notIn = keys
		set.hasNot = true
	}
	if _, exists := m["exists"]; exists && !set.finite && !set.hasNot {
		return atomSet{}, false
	}
	return set, true
}

func leafDisjoint(a, b atomSet) bool {
	af, aFinite := acceptAtoms(a)
	bf, bFinite := acceptAtoms(b)
	switch {
	case aFinite && bFinite:
		return !intersects(af, bf)
	case aFinite && !b.finite && b.hasNot:
		return subset(af, b.notIn)
	case bFinite && !a.finite && a.hasNot:
		return subset(bf, a.notIn)
	default:
		return false
	}
}

func acceptAtoms(s atomSet) ([]string, bool) {
	if !s.finite {
		return nil, false
	}
	if !s.hasNot {
		return s.in, true
	}
	var out []string
	for _, v := range s.in {
		if !containsStr(s.notIn, v) {
			out = append(out, v)
		}
	}
	return out, true
}

func atomKeys(items []any) ([]string, bool) {
	out := make([]string, 0, len(items))
	for _, item := range items {
		k, ok := ScalarKey(item)
		if !ok {
			return nil, false
		}
		out = append(out, k)
	}
	return out, true
}

func intersects(a, b []string) bool {
	for _, v := range a {
		if containsStr(b, v) {
			return true
		}
	}
	return false
}

func subset(a, b []string) bool {
	for _, v := range a {
		if !containsStr(b, v) {
			return false
		}
	}
	return true
}

func containsStr(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func diagHasError(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

func filterError(format string, args ...any) Diagnostic {
	return Diagnostic{Line: 1, Severity: SeverityError, Message: fmt.Sprintf(format, args...)}
}

func exactKeys(keys ...string) map[string]FilterKind {
	m := make(map[string]FilterKind, len(keys))
	for _, k := range keys {
		m[k] = FilterExact
	}
	return m
}

func buildFilterProviders() map[string]filterProvider {
	gh := exactKeys("repos", "branches", "base_branches", "labels", "authors", "assignees", "mentioned", "reviewers", "reviewer_teams", "review_states", "workflows", "conclusions", "pr_numbers")
	gh["paths"] = FilterGlob
	gh["keywords"] = FilterKeyword

	slack := exactKeys("channel_ids", "channels", "user_ids", "users", "emojis", "item_user_ids", "itemUsers")
	slack["keywords"] = FilterKeyword

	linear := exactKeys("team_ids", "teams", "project_ids", "projects", "state_ids", "states", "issue_ids", "issues", "labels", "assignee_ids", "creator_ids", "mentioned_user_ids")
	linear["keywords"] = FilterKeyword

	jira := exactKeys("project_keys", "labels", "status_ids")
	jira["keywords"] = FilterKeyword

	gl := exactKeys("repos", "actions", "branches", "base_branches", "mentioned")
	az := exactKeys("repos", "source_repos", "branches", "base_branches", "work_item_types", "labels", "authors", "assignees", "mentioned")
	wh := exactKeys("webhook_ids")

	return map[string]filterProvider{
		"github":       {keys: gh},
		"gitlab":       {keys: gl},
		"azure_devops": {keys: az},
		"slack": {keys: slack, aliases: map[string]string{
			"channels":  "channel_ids",
			"users":     "user_ids",
			"itemUsers": "item_user_ids",
		}},
		"teams": {},
		"linear": {keys: linear, aliases: map[string]string{
			"teams":    "team_ids",
			"projects": "project_ids",
			"states":   "state_ids",
			"issues":   "issue_ids",
		}},
		"jira":     {keys: jira},
		"schedule": {},
		"webhook":  {keys: wh},
		"factory":  {keys: exactKeys("stages")},
	}
}

var filterProviders = buildFilterProviders()
