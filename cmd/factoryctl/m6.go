package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/polds/factory/internal/client"
)

func cmdScorers(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		scorersUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdScorersList(args[1:], stdout, stderr, getenv)
	case "results":
		return cmdScorersResults(args[1:], stdout, stderr, getenv)
	default:
		scorersUsage(stderr)
		return 2
	}
}

func scorersUsage(w io.Writer) {
	say(w, `usage: factoryctl scorers <command>

Commands:
  list --factory NAME
  results --factory NAME SCORER [--from TIME] [--to TIME] [--passed true|false]`)
}

func cmdScorersList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("scorers list", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.Scorers(context.Background(), factoryName)
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s agents=%s passing_score=%g sampling_rate=%d model=%s self_improvement=%t\n", row.Name, strings.Join(row.Agents, ","), row.PassingScore, row.SamplingRate, row.Model, row.SelfImprovement); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdScorersResults(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("scorers results", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fromFlag := fs.String("from", "", "window start, RFC3339")
	toFlag := fs.String("to", "", "window end, RFC3339")
	passedFlag := fs.String("passed", "", "true or false")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl scorers results --factory NAME SCORER [--from TIME] [--to TIME] [--passed true|false]")
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
	if strings.TrimSpace(*factoryName) == "" || len(pos) != 1 {
		fs.Usage()
		return 2
	}
	from, ok := parseCLITime(stderr, "from", *fromFlag)
	if !ok {
		return 2
	}
	to, ok := parseCLITime(stderr, "to", *toFlag)
	if !ok {
		return 2
	}
	var passed *bool
	switch strings.TrimSpace(*passedFlag) {
	case "":
	case "true":
		v := true
		passed = &v
	case "false":
		v := false
		passed = &v
	default:
		say(stderr, "factoryctl: passed is invalid")
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.ScorerResults(context.Background(), *factoryName, pos[0], client.ScorerResultQuery{
		From: from, To: to, Passed: passed,
	})
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s run=%s label=%s score=%g passed=%t status=%s\n", row.ID, row.RunID, row.Label, row.Score, row.Passed, row.Status); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdRunsScore(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("runs score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scorer := fs.String("scorer", "", "scorer directory slug")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl runs score RUN_ID --scorer NAME")
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
	if len(pos) != 1 || strings.TrimSpace(*scorer) == "" {
		fs.Usage()
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	accepted, err := c.ScoreRun(context.Background(), pos[0], strings.TrimSpace(*scorer))
	if err != nil {
		return exitAPI(stderr, err)
	}
	if _, err := fmt.Fprintf(stdout, "%s judge_run=%s\n", accepted.ResultID, accepted.JudgeRunID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdRunsScores(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("runs scores", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl runs scores RUN_ID")
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
	if len(pos) != 1 {
		fs.Usage()
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.RunScores(context.Background(), pos[0])
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s %s %g %t %s\n", row.Scorer, row.Label, row.Score, row.Passed, row.Status); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdRunsCapture(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("runs capture-task", flag.ContinueOnError)
	fs.SetOutput(stderr)
	suite := fs.String("suite", "", "benchmark suite")
	name := fs.String("name", "", "task name")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl runs capture-task RUN_ID --suite SUITE --name NAME")
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
	if len(pos) != 1 || strings.TrimSpace(*suite) == "" || strings.TrimSpace(*name) == "" {
		fs.Usage()
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	task, err := c.CaptureBenchmarkTask(context.Background(), pos[0], client.CaptureBenchmarkRequest{
		Suite: strings.TrimSpace(*suite),
		Name:  strings.TrimSpace(*name),
	})
	if err != nil {
		return exitAPI(stderr, err)
	}
	if _, err := io.WriteString(stdout, task.Content); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdBenchmarks(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		benchmarksUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdBenchmarksList(args[1:], stdout, stderr, getenv)
	case "run":
		return cmdBenchmarksRun(args[1:], stdout, stderr, getenv)
	case "trials":
		return cmdBenchmarksTrials(args[1:], stdout, stderr, getenv)
	default:
		benchmarksUsage(stderr)
		return 2
	}
}

func benchmarksUsage(w io.Writer) {
	say(w, `usage: factoryctl benchmarks <command>

Commands:
  list --factory NAME
  run --factory NAME SUITE [--repetitions N]
  trials --factory NAME SUITE [--benchmark-run-id ID]`)
}

func cmdBenchmarksList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("benchmarks list", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.Benchmarks(context.Background(), factoryName)
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, row := range rows {
		last := ""
		if row.LastRun != nil {
			last = row.LastRun.ID
		}
		if _, err := fmt.Fprintf(stdout, "%s agent=%s configurations=%s tasks=%s last_run=%s\n", row.Suite, row.Agent, strings.Join(row.Configurations, ","), strings.Join(row.Tasks, ","), last); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdBenchmarksRun(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("benchmarks run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	repsFlag := fs.String("repetitions", "", "trial repetitions")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl benchmarks run --factory NAME SUITE [--repetitions N]")
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
	if strings.TrimSpace(*factoryName) == "" || len(pos) != 1 {
		fs.Usage()
		return 2
	}
	var reps *int
	if strings.TrimSpace(*repsFlag) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(*repsFlag))
		if err != nil {
			say(stderr, "factoryctl: repetitions is invalid")
			return 2
		}
		reps = &n
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	accepted, err := c.StartBenchmark(context.Background(), *factoryName, pos[0], reps)
	if err != nil {
		return exitAPI(stderr, err)
	}
	if _, err := fmt.Fprintln(stdout, accepted.BenchmarkRunID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdBenchmarksTrials(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("benchmarks trials", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	runID := fs.String("benchmark-run-id", "", "benchmark run id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl benchmarks trials --factory NAME SUITE [--benchmark-run-id ID]")
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
	if strings.TrimSpace(*factoryName) == "" || len(pos) != 1 {
		fs.Usage()
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	view, err := c.BenchmarkTrials(context.Background(), *factoryName, pos[0], *runID)
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, cell := range view.Cells {
		if _, err := fmt.Fprintf(stdout, "%s %s n=%d pass=%d errored=%d pass_rate=%g\n", cell.Configuration, cell.Task, cell.N, cell.Pass, cell.Errored, cell.PassRate); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	if _, err := fmt.Fprintf(stdout, "winner=%s reason=%s\n", view.Report.Winner, view.Report.Reason); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdImprovement(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		improvementUsage(stderr)
		return 2
	}
	switch args[0] {
	case "batches":
		return cmdImprovementBatches(args[1:], stdout, stderr, getenv)
	case "run-now":
		return cmdImprovementRunNow(args[1:], stdout, stderr, getenv)
	default:
		improvementUsage(stderr)
		return 2
	}
}

func improvementUsage(w io.Writer) {
	say(w, `usage: factoryctl improvement <command>

Commands:
  batches --factory NAME
  run-now --factory NAME`)
}

func cmdImprovementBatches(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("improvement batches", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	rows, err := c.ImprovementBatches(context.Background(), factoryName)
	if err != nil {
		return exitAPI(stderr, err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(stdout, "%s agent=%s status=%s pr=%s failures=%d\n", row.ID, row.Agent, row.Status, row.PRURL, len(row.Failures)); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdImprovementRunNow(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, ok := factoryFlag("improvement run-now", args, stderr)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	out, err := c.RunImprovementNow(context.Background(), factoryName)
	if err != nil {
		return exitAPI(stderr, err)
	}
	if _, err := fmt.Fprintf(stdout, "checked=%s\n", strings.Join(out.CheckedAgents, ",")); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, row := range out.Batches {
		if _, err := fmt.Fprintf(stdout, "%s agent=%s status=%s\n", row.ID, row.Agent, row.Status); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdMetrics(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fromFlag := fs.String("from", "", "window start, RFC3339")
	toFlag := fs.String("to", "", "window end, RFC3339")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl metrics --factory NAME [--from TIME] [--to TIME]")
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
	if strings.TrimSpace(*factoryName) == "" || len(pos) != 0 {
		fs.Usage()
		return 2
	}
	from, ok := parseCLITime(stderr, "from", *fromFlag)
	if !ok {
		return 2
	}
	to, ok := parseCLITime(stderr, "to", *toFlag)
	if !ok {
		return 2
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	view, err := c.Metrics(context.Background(), *factoryName, from, to)
	if err != nil {
		return exitAPI(stderr, err)
	}
	if _, err := fmt.Fprintf(stdout, "prs_opened=%d prs_merged=%d\nautonomy share=%g n=%d\ncost_per_pr median_cents=%d\n", view.PRsOpened, view.PRsMerged, view.Autonomy.Share, view.Autonomy.N, view.CostPerPR.MedianCents); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func parseCLITime(stderr io.Writer, name, v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		say(stderr, "factoryctl:", name, "is invalid")
		return time.Time{}, false
	}
	return parsed, true
}

func exitAPI(stderr io.Writer, err error) int {
	if strings.Contains(err.Error(), "api 501") {
		say(stderr, "factoryctl: not available on this factoryd")
		return 1
	}
	say(stderr, "factoryctl:", err)
	return 1
}
