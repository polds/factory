package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/polds/factory/internal/client"
)

func cmdDeliveries(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		deliveriesUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdDeliveriesList(args[1:], stdout, stderr, getenv)
	case "replay":
		return cmdDeliveriesReplay(args[1:], stdout, stderr, getenv)
	case "-h", "--help", "help":
		deliveriesUsage(stdout)
		return 0
	default:
		deliveriesUsage(stderr)
		return 2
	}
}

func deliveriesUsage(w io.Writer) {
	say(w, `usage: factoryctl deliveries <command>

Commands:
  list --factory NAME [--unmatched]
  replay DELIVERY_ID`)
}

func cmdDeliveriesList(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("deliveries list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name or id")
	unmatched := fs.Bool("unmatched", false, "only unmatched deliveries")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl deliveries list --factory NAME [--unmatched]")
	}
	flagArgs, pos, err := reorderArgs(args, map[string]bool{"unmatched": true, "h": true, "help": true})
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
	rows, err := c.ListDeliveries(context.Background(), *factoryName, client.DeliveryFilter{Unmatched: *unmatched})
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(stdout, formatDelivery(row)); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	return 0
}

func cmdDeliveriesReplay(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("deliveries replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl deliveries replay DELIVERY_ID")
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
		say(stderr, "factoryctl: replay requires DELIVERY_ID")
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	row, err := c.ReplayDelivery(context.Background(), pos[0])
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\n", row.ID, row.Status); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func formatDelivery(row client.Delivery) string {
	return fmt.Sprintf("%s provider=%s status=%s delivery=%s", row.ID, row.Provider, row.Status, row.DeliveryID)
}
