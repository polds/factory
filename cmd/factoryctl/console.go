package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/polds/factory/internal/client"
)

func cmdRuns(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		runsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "show":
		return cmdRunsShow(args[1:], stdout, stderr, getenv)
	case "interrupt":
		return cmdRunsInterrupt(args[1:], stdout, stderr, getenv)
	case "followup":
		return cmdRunsFollowup(args[1:], stdout, stderr, getenv)
	case "score":
		return cmdRunsScore(args[1:], stdout, stderr, getenv)
	case "scores":
		return cmdRunsScores(args[1:], stdout, stderr, getenv)
	case "capture-task":
		return cmdRunsCapture(args[1:], stdout, stderr, getenv)
	default:
		runsUsage(stderr)
		return 2
	}
}

func runsUsage(w io.Writer) {
	say(w, `usage: factoryctl runs <command>

Commands:
  show RUN_ID
  interrupt RUN_ID
  followup RUN_ID --text TEXT
  score RUN_ID --scorer NAME
  scores RUN_ID
  capture-task RUN_ID --suite SUITE --name NAME`)
}

func cmdRunsShow(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	id, ok := oneArg("runs show", "RUN_ID", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	detail, err := c.RunDetail(context.Background(), id)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\nclass: %s\nsummary: %s\n", detail.ID, detail.Status, detail.Failure.Class, detail.Failure.Summary); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, line := range detail.Failure.Evidence {
		if _, err := fmt.Fprintf(stdout, "evidence: %s\n", line); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdRunsInterrupt(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	id, ok := oneArg("runs interrupt", "RUN_ID", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	steerID, err := c.Interrupt(context.Background(), id)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, steerID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdRunsFollowup(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("runs followup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "follow-up text")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl runs followup RUN_ID --text TEXT")
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) != 1 || strings.TrimSpace(*text) == "" {
		if len(pos) != 1 {
			say(stderr, "factoryctl: runs followup requires RUN_ID")
		}
		if strings.TrimSpace(*text) == "" {
			say(stderr, "factoryctl: --text is required")
		}
		fs.Usage()
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	steerID, err := c.Followup(context.Background(), pos[0], *text)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, steerID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdInbox(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("inbox", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	items, err := c.Inbox(context.Background(), factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, item := range items {
		runID := item.RunID
		if runID == "" {
			runID = "-"
		}
		if _, err := fmt.Fprintf(stdout, "%s %s work_item=%s run=%s title=%s\n", item.Kind, item.ID, item.WorkItemID, runID, item.Title); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdWorkers(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if !noArgs("workers", args, stderr) {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	view, err := c.Workers(context.Background())
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, worker := range view.Workers {
		line := fmt.Sprintf("worker %s pool=%s platform=%s backend=%s version=%s in_flight=%d capacity=%d heartbeat_age_ms=%d reconnects=%d rejected_1h=%d",
			worker.ID, worker.Pool, worker.Platform, worker.Backend, worker.Version, worker.InFlight, worker.Capacity, worker.HeartbeatAgeMS, worker.Reconnects, worker.Rejected1h)
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	for _, pool := range view.Pools {
		if _, err := fmt.Fprintf(stdout, "pool %s queue_depth=%d p50_time_to_assign_ms=%d rejected_1h=%d\n", pool.Pool, pool.QueueDepth, pool.P50TimeToAssign, pool.Rejected1h); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdAutomations(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		automationsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdAutomationsList(args[1:], stdout, stderr, getenv)
	case "pause":
		return cmdAutomationsSet(args[1:], true, stdout, stderr, getenv)
	case "resume":
		return cmdAutomationsSet(args[1:], false, stdout, stderr, getenv)
	default:
		automationsUsage(stderr)
		return 2
	}
}

func automationsUsage(w io.Writer) {
	say(w, `usage: factoryctl automations <command>

Commands:
  list --factory NAME
  pause --factory NAME AUTOMATION
  resume --factory NAME AUTOMATION`)
}

func cmdAutomationsList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("automations list", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.Automations(context.Background(), factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s enabled=%t runtime_paused=%t matches=%d runs_started=%d\n", row.Name, row.Enabled, row.RuntimePaused, row.MatchCount, row.RunsStarted); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdAutomationsSet(args []string, pause bool, stdout, stderr io.Writer, getenv func(string) string) int {
	cmd := "automations resume"
	verb := "resumed"
	if pause {
		cmd = "automations pause"
		verb = "paused"
	}
	factoryName, name, ok := parseFactoryAndName(cmd, "AUTOMATION", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	var err error
	if pause {
		err = c.PauseAutomation(context.Background(), factoryName, name)
	} else {
		err = c.ResumeAutomation(context.Background(), factoryName, name)
	}
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\n", name, verb); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdSecrets(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] != "list" {
		say(stderr, "usage: factoryctl secrets list --factory NAME")
		return 2
	}
	factoryName, ok := factoryFlag("secrets list", args[1:], stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.Secrets(context.Background(), factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s provider=%s present=%t\n", row.Name, row.Provider, row.Present); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdDefinition(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] != "show" {
		say(stderr, "usage: factoryctl definition show --factory NAME")
		return 2
	}
	factoryName, ok := factoryFlag("definition show", args[1:], stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	view, err := c.Definition(context.Background(), factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "source: %s\ngit_sha: %s\nproduction_ref: %s\neditable: %t\n", view.Source, view.GitSHA, view.ProductionRef, view.Editable); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	// Content is the definition source and is printed verbatim. Secret
	// values are not a field of this response; an extra JSON "value" is
	// dropped because DefinitionFile has no such field.
	for _, file := range view.Files {
		if _, err := fmt.Fprintf(stdout, "--- %s\n%s", file.Path, file.Content); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if !strings.HasSuffix(file.Content, "\n") {
			if _, err := fmt.Fprintln(stdout); err != nil {
				say(stderr, "factoryctl:", err)
				return 1
			}
		}
	}
	return 0
}

func clientOrExit(stderr io.Writer, getenv func(string) string) (*client.Client, int) {
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return nil, 2
	}
	return c, 0
}

func oneArg(cmd, what string, args []string, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl", cmd, what)
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return "", false
	}
	if err := fs.Parse(flagArgs); err != nil {
		return "", false
	}
	if len(pos) != 1 {
		say(stderr, "factoryctl:", cmd, "requires", what)
		fs.Usage()
		return "", false
	}
	return pos[0], true
}

func noArgs(cmd string, args []string, stderr io.Writer) bool {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl", cmd)
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return false
	}
	if err := fs.Parse(flagArgs); err != nil {
		return false
	}
	if len(pos) != 0 {
		fs.Usage()
		return false
	}
	return true
}

func factoryFlag(cmd string, args []string, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl", cmd, "--factory NAME")
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return "", false
	}
	if err := fs.Parse(flagArgs); err != nil {
		return "", false
	}
	if len(pos) != 0 || *factoryName == "" {
		if *factoryName == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		fs.Usage()
		return "", false
	}
	return *factoryName, true
}

func parseFactoryAndName(cmd, what string, args []string, stderr io.Writer) (string, string, bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl", cmd, "--factory NAME", what)
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return "", "", false
	}
	if err := fs.Parse(flagArgs); err != nil {
		return "", "", false
	}
	if *factoryName == "" || len(pos) != 1 {
		if *factoryName == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		if len(pos) != 1 {
			say(stderr, "factoryctl:", cmd, "requires", what)
		}
		fs.Usage()
		return "", "", false
	}
	return *factoryName, pos[0], true
}
