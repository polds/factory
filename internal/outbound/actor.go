package outbound

import "strings"

// ReplyActor is the person named on a follow-up run's prompt.
// The last untrusted fence is the reply. A non-empty actor wins, then
// actor_id. Lines after that fence's text marker are the reply body and
// cannot supply either field.
func ReplyActor(prompt string) string {
	const marker = "<<<UNTRUSTED "
	i := strings.LastIndex(prompt, marker)
	if i < 0 {
		return ""
	}
	fence := prompt[i:]
	if cut := strings.Index(fence, "\ntext:"); cut >= 0 {
		fence = fence[:cut]
	}
	if actor := lastField(fence, "actor"); actor != "" {
		return actor
	}
	return lastField(fence, "actor_id")
}

func lastField(fence, label string) string {
	prefix := label + ": "
	var last string
	for _, line := range strings.Split(fence, "\n") {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest != "" {
			last = rest
		}
	}
	return last
}
