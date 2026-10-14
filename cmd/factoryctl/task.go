package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/polds/factory/internal/client"
)

func cmdTask(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		taskUsage(stderr)
		return 2
	}
	switch args[0] {
	case "create":
		return cmdTaskCreate(args[1:], stdout, stderr, getenv)
	case "list":
		return cmdTaskList(args[1:], stdout, stderr, getenv)
	case "get":
		return cmdTaskGet(args[1:], stdout, stderr, getenv)
	case "cancel":
		return cmdTaskCancel(args[1:], stdout, stderr, getenv)
	case "watch":
		return cmdTaskWatch(args[1:], stdout, stderr, getenv)
	default:
		taskUsage(stderr)
		return 2
	}
}

func taskUsage(w io.Writer) {
	say(w, `usage: factoryctl task <command>

Commands:
  create --factory NAME --title TEXT [--body TEXT] [--source issue|manual|api] [--ref REF]
  list --factory NAME [--stage STAGE] [--status STATUS]
  get --factory NAME TASK_ID
  cancel --factory NAME TASK_ID
  watch --factory NAME TASK_ID`)
}

func cmdTaskCreate(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	title := fs.String("title", "", "work item title")
	body := fs.String("body", "", "work item body")
	source := fs.String("source", "", "issue, manual, or api")
	ref := fs.String("ref", "", "source reference")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl task create --factory NAME --title TEXT [--body TEXT] [--source issue|manual|api] [--ref REF]")
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
	if len(pos) != 0 || *factoryName == "" || *title == "" {
		if *factoryName == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		if *title == "" {
			say(stderr, "factoryctl: --title is required")
		}
		fs.Usage()
		return 2
	}
	if !validTaskSource(*source) {
		say(stderr, "factoryctl: --source must be issue, manual, or api")
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	item, err := c.CreateTask(context.Background(), *factoryName, client.TaskRequest{
		Title:     *title,
		Body:      *body,
		Source:    *source,
		SourceRef: *ref,
	})
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, item.ID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdTaskList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	stage := fs.String("stage", "", "stage filter")
	status := fs.String("status", "", "status filter")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl task list --factory NAME [--stage STAGE] [--status STATUS]")
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
	if len(pos) != 0 || *factoryName == "" {
		if *factoryName == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	items, err := c.ListTasks(context.Background(), *factoryName, client.TaskFilter{Stage: *stage, Status: *status})
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, item := range items {
		if _, err := fmt.Fprintln(stdout, formatWorkItem(item)); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdTaskGet(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, id, ok := parseFactoryAndID("task get", args, stderr)
	if !ok {
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	item, err := c.GetTask(context.Background(), factoryName, id)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, formatWorkItem(item)); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdTaskCancel(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, id, ok := parseFactoryAndID("task cancel", args, stderr)
	if !ok {
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	item, err := c.CancelTask(context.Background(), factoryName, id)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\n", item.ID, item.Status); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func cmdTaskWatch(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	factoryName, id, ok := parseFactoryAndID("task watch", args, stderr)
	if !ok {
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	code, err := c.WatchTask(context.Background(), factoryName, id, stdout)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return code
}

func parseFactoryAndID(cmd string, args []string, stderr io.Writer) (string, string, bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl", cmd, "--factory NAME TASK_ID")
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
			say(stderr, "factoryctl:", cmd, "requires TASK_ID")
		}
		fs.Usage()
		return "", "", false
	}
	return *factoryName, pos[0], true
}

func formatWorkItem(item client.WorkItem) string {
	line := fmt.Sprintf("%s stage=%s status=%s title=%s", item.ID, item.Stage, item.Status, item.Title)
	if item.PendingGate != "" {
		line += " gate=" + item.PendingGate
	}
	return line
}

func validTaskSource(source string) bool {
	switch source {
	case "", "issue", "manual", "api":
		return true
	default:
		return false
	}
}

var helpFlags = map[string]bool{"h": true, "help": true}
