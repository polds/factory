package improve

import (
	"fmt"
	"strings"

	"github.com/polds/factory/internal/harness"
)

// Batch is the improvement proposal PRBody describes. Secrets are removed
// from the body with harness.Redact.
type Batch struct {
	Agent   string
	Layer   string
	Secrets []string
}

// Links resolves a run id and a scorer-result id to URLs. A nil func, or a
// func that returns empty, leaves the id as the only reference.
type Links struct {
	Run    func(runID string) string
	Result func(resultID string) string
}

// PRBody is the improvement pull request body. It lists every run and
// scorer result under "## Regressions addressed", names the layer, and
// closes with the loop footer.
func PRBody(b Batch, fs []Failure, links Links) string {
	var buf strings.Builder
	buf.WriteString("## Regressions addressed\n\n")
	for _, f := range fs {
		runRef := linkOrID(links.Run, f.RunID)
		resultRef := linkOrID(links.Result, f.ResultID)
		fmt.Fprintf(&buf, "- run %s (%s), scorer result %s (%s), scorer %s, label %s\n", f.RunID, runRef, f.ResultID, resultRef, f.Scorer, f.Label)
	}
	fmt.Fprintf(&buf, "\nLayer changed: %s\n\n", b.Layer)
	buf.WriteString("Opened by the factory self-improvement loop. Merging marks these failures reviewed. Closing without merge marks them reviewed and records the rejection so the next improvement run can see it.\n")
	return harness.Redact(buf.String(), b.Secrets)
}

func linkOrID(fn func(string) string, id string) string {
	if fn == nil {
		return id
	}
	if u := fn(id); u != "" {
		return u
	}
	return id
}
