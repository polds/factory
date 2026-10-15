package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/polds/factory/internal/client"
)

func cmdSchedules(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		schedulesUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdSchedulesList(args[1:], stdout, stderr, getenv)
	case "pause":
		return cmdSchedulePaused(args[1:], true, stdout, stderr, getenv)
	case "resume":
		return cmdSchedulePaused(args[1:], false, stdout, stderr, getenv)
	case "-h", "--help", "help":
		schedulesUsage(stdout)
		return 0
	default:
		schedulesUsage(stderr)
		return 2
	}
}

func schedulesUsage(w io.Writer) {
	say(w, `usage: factoryctl schedules <command>

Commands:
  list [--factory NAME]
  pause SCHEDULE_ID
  resume SCHEDULE_ID`)
}

func cmdSchedulesList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("schedules list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl schedules list [--factory NAME]")
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
	if len(pos) != 0 {
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	rows, err := c.ListSchedules(context.Background(), *factoryName)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(stdout, formatSchedule(row)); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdSchedulePaused(args []string, pause bool, stdout, stderr io.Writer, getenv func(string) string) int {
	verb := "resume"
	if pause {
		verb = "pause"
	}
	fs := flag.NewFlagSet("schedules "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl schedules", verb, "SCHEDULE_ID")
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
		say(stderr, "factoryctl: schedules", verb, "requires SCHEDULE_ID")
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	var row client.Schedule
	if pause {
		row, err = c.PauseSchedule(context.Background(), pos[0])
	} else {
		row, err = c.ResumeSchedule(context.Background(), pos[0])
	}
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, formatSchedule(row)); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func formatSchedule(row client.Schedule) string {
	name := row.Name
	if name == "" {
		name = "-"
	}
	return fmt.Sprintf("%s automation=%s name=%s cron=%q paused=%t", row.ID, row.Automation, name, row.Cron, row.Paused)
}
