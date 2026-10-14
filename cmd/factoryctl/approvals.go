package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

func cmdApprovals(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		approvalsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdApprovalsList(args[1:], stdout, stderr, getenv)
	case "decide":
		return cmdApprovalsDecide(args[1:], stdout, stderr, getenv)
	default:
		approvalsUsage(stderr)
		return 2
	}
}

func approvalsUsage(w io.Writer) {
	say(w, `usage: factoryctl approvals <command>

Commands:
  list --factory NAME
  decide APPROVAL_ID --decision approve|reject [--note TEXT]`)
}

func cmdApprovalsList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("approvals list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl approvals list --factory NAME")
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
	items, err := c.ListApprovals(context.Background(), *factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(stdout, "%s gate=%s work_item=%s\n", item.ID, item.Gate, item.WorkItemID); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdApprovalsDecide(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("approvals decide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	decision := fs.String("decision", "", "approve or reject")
	note := fs.String("note", "", "decision note")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl approvals decide APPROVAL_ID --decision approve|reject [--note TEXT]")
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
		say(stderr, "factoryctl: approvals decide requires APPROVAL_ID")
		fs.Usage()
		return 2
	}
	if !validDecision(*decision) {
		say(stderr, "factoryctl: --decision must be approve or reject")
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	item, err := c.DecideApproval(context.Background(), pos[0], *decision, *note)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\n", item.ID, item.Decision); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func validDecision(decision string) bool {
	switch decision {
	case "approve", "reject":
		return true
	default:
		return false
	}
}
