package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/polds/factory/internal/client"
)

func cmdRun(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prompt := fs.String("prompt", "", "task prompt")
	factoryName := fs.String("factory", "", "factory name or id")
	agent := fs.String("agent", "", "agent name")
	title := fs.String("title", "", "run title")
	watch := fs.Bool("watch", false, "stream the transcript after starting")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl run --prompt TEXT [--factory NAME] [--agent NAME] [--title TITLE] [--watch]")
	}
	flagArgs, pos, err := reorderArgs(args, map[string]bool{"watch": true, "h": true, "help": true})
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) != 0 || *prompt == "" {
		if *prompt == "" {
			say(stderr, "factoryctl: --prompt is required")
		}
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	run, err := c.StartRun(context.Background(), client.RunRequest{
		Factory: *factoryName,
		Agent:   *agent,
		Prompt:  *prompt,
		Title:   *title,
	})
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, run.ID); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if !*watch {
		return 0
	}
	code, err := c.Watch(context.Background(), run.ID, nil, stdout)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return code
}

func cmdWatch(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.Int64("from-seq", 0, "resume after this event seq")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl watch RUN_ID [--from-seq N]")
	}
	flagArgs, pos, err := reorderArgs(args, map[string]bool{"h": true, "help": true})
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) != 1 {
		say(stderr, "factoryctl: watch requires RUN_ID")
		fs.Usage()
		return 2
	}
	var fromSeq *int64
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "from-seq" {
			v := *from
			fromSeq = &v
		}
	})
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	code, err := c.Watch(context.Background(), pos[0], fromSeq, stdout)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return code
}

func cmdCancel(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl cancel RUN_ID")
	}
	flagArgs, pos, err := reorderArgs(args, map[string]bool{"h": true, "help": true})
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) != 1 {
		say(stderr, "factoryctl: cancel requires RUN_ID")
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	run, err := c.CancelRun(context.Background(), pos[0])
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s %s\n", run.ID, run.Status); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}

func clientFromEnv(getenv func(string) string) (*client.Client, error) {
	if getenv == nil {
		return nil, errors.New("FACTORY_API_KEY is required")
	}
	key := getenv("FACTORY_API_KEY")
	if key == "" {
		return nil, errors.New("FACTORY_API_KEY is required")
	}
	base := getenv("FACTORY_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	return &client.Client{BaseURL: base, APIKey: key}, nil
}

// reorderArgs pulls flags in front of positionals so RUN_ID can precede --from-seq.
func reorderArgs(args []string, boolFlags map[string]bool) ([]string, []string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		flags = append(flags, a)
		if hasValue || boolFlags[name] {
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("flag needs an argument: --%s", name)
		}
		i++
		flags = append(flags, args[i])
	}
	return flags, pos, nil
}
