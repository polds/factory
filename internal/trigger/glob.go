package trigger

import (
	"path/filepath"
	"strings"
)

const maxGlobSteps = 10000

// MatchGlob reports whether name matches a doublestar pattern.
// Separators are slashes. * and ? do not cross a separator. ** matches
// zero or more segments. Character classes support ^ and ! negation.
// A pattern or name that is too long, or a walk that exceeds the step
// cap, does not match.
func MatchGlob(pattern, name string) bool {
	if len(pattern) > 1024 || len(name) > 4096 {
		return false
	}
	name = strings.ReplaceAll(name, "\\", "/")
	steps := 0
	return matchParts(splitGlob(pattern), splitGlob(name), &steps)
}

func splitGlob(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchParts(pat, name []string, steps *int) bool {
	*steps++
	if *steps > maxGlobSteps {
		return false
	}
	for len(pat) > 0 {
		if pat[0] == "**" {
			if matchParts(pat[1:], name, steps) {
				return true
			}
			if len(name) == 0 || *steps > maxGlobSteps {
				return false
			}
			name = name[1:]
			continue
		}
		if len(name) == 0 {
			return false
		}
		if !matchSegment(pat[0], name[0]) {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	return len(name) == 0
}

func matchSegment(pat, name string) bool {
	pat = strings.ReplaceAll(pat, "[!", "[^")
	ok, err := filepath.Match(pat, name)
	return err == nil && ok
}
