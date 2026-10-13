package harness

import "strings"

// flagValue marks a flag that consumes the next argv element.
// flagBare marks a flag that does not.
// flagOptional marks a flag that consumes the next element only when that
// element is present and is not itself a flag. Upstream emits a bare
// --idle-on-complete when neither the task nor the worker sets a duration
// (oz-agent-worker 63dbb8a, task_utils_test.go "uses oz cli default").
const (
	kindValue = iota
	kindBare
	kindOptional
)

// knownFlags is the oz vocabulary from docs/05. Unknown flags are warnings.
var knownFlags = map[string]int{
	"task-id":                       kindValue,
	"model":                         kindValue,
	"harness":                       kindValue,
	"profile":                       kindValue,
	"skill":                         kindValue,
	"mcp":                           kindValue,
	"environment":                   kindValue,
	"server-root-url":               kindValue,
	"session-sharing-server-url":    kindValue,
	"share":                         kindValue,
	"idle-on-complete":              kindOptional,
	"computer-use-model":            kindValue,
	"repository-head-override-json": kindValue,
	"snapshot-upload-timeout":       kindValue,
	"snapshot-script-timeout":       kindValue,
	"bedrock-inference-role":        kindValue,
	"bedrock-role-region":           kindValue,
	"add":                           kindValue,
	"sandboxed":                     kindBare,
	"bare":                          kindBare,
	"computer-use":                  kindBare,
	"no-computer-use":               kindBare,
	"skip-initial-turn":             kindBare,
	"remove-repository-origins":     kindBare,
	"no-snapshot":                   kindBare,
	"global":                        kindBare,
}

// Invocation is the parsed oz command line.
type Invocation struct {
	Positionals []string
	Values      map[string][]string
	Bools       map[string]bool
	Warnings    []string
}

// TaskID returns the last --task-id value.
func (in Invocation) TaskID() string { return in.last("task-id") }

// Model returns the last --model value.
func (in Invocation) Model() string { return in.last("model") }

// HarnessName returns the last --harness value.
func (in Invocation) HarnessName() string { return in.last("harness") }

// IdleOnComplete returns the last duration, or empty when the flag is bare,
// empty, or absent. Empty means the conversation should exit when it ends.
func (in Invocation) IdleOnComplete() string { return in.last("idle-on-complete") }

// SkipInitialTurn reports --skip-initial-turn.
func (in Invocation) SkipInitialTurn() bool { return in.Bools["skip-initial-turn"] }

func (in Invocation) last(name string) string {
	v := in.Values[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// ParseArgs accepts the worker's argv, which starts with the positionals
// "agent" and "run". Unknown flags produce a warning and are dropped.
func ParseArgs(args []string) Invocation {
	in := Invocation{
		Values: map[string][]string{},
		Bools:  map[string]bool{},
	}
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if a == "--" {
			in.Positionals = append(in.Positionals, args...)
			for _, p := range args {
				in.Warnings = append(in.Warnings, "ignoring positional argument "+p)
			}
			break
		}
		if !strings.HasPrefix(a, "--") || a == "--" {
			in.Positionals = append(in.Positionals, a)
			in.Warnings = append(in.Warnings, "ignoring positional argument "+a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		value := ""
		hasValue := false
		if i := strings.IndexByte(name, '='); i >= 0 {
			value = name[i+1:]
			name = name[:i]
			hasValue = true
		}
		kind, known := knownFlags[name]
		if !known {
			if !hasValue && len(args) > 0 && !strings.HasPrefix(args[0], "--") && args[0] != "" {
				in.Warnings = append(in.Warnings, "ignoring unknown flag --"+name+" "+args[0])
				args = args[1:]
				continue
			}
			in.Warnings = append(in.Warnings, "ignoring unknown flag --"+name)
			continue
		}
		switch kind {
		case kindBare:
			in.Bools[name] = true
		case kindValue:
			if !hasValue {
				if len(args) == 0 || strings.HasPrefix(args[0], "--") {
					value = ""
				} else {
					value = args[0]
					args = args[1:]
				}
			}
			in.Values[name] = append(in.Values[name], value)
		case kindOptional:
			if !hasValue {
				if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
					value = args[0]
					args = args[1:]
				}
			}
			in.Values[name] = append(in.Values[name], value)
		default:
			in.Warnings = append(in.Warnings, "ignoring unknown flag --"+name)
		}
	}
	return in
}
