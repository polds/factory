package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"

	"github.com/polds/factory/internal/client"
)

func cmdInvoke(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		invokeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "create":
		return cmdInvokeCreate(args[1:], stdout, stderr, getenv)
	default:
		invokeUsage(stderr)
		return 2
	}
}

func invokeUsage(w io.Writer) {
	say(w, `usage: factoryctl invoke <command>

Commands:
  create --factory NAME --key KEY [--intent new|continue|wake] [--priority interactive|batch] [--profile NAME]`)
}

func cmdInvokeCreate(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("invoke create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factory := fs.String("factory", "", "factory name or uid")
	key := fs.String("key", "", "source occurrence key")
	intent := fs.String("intent", "new", "invocation intent")
	priority := fs.String("priority", "interactive", "interactive or batch")
	profile := fs.String("profile", "", "profile name")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl invoke create --factory NAME --key KEY [--intent new|continue|wake] [--priority interactive|batch] [--profile NAME]")
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
		say(stderr, "factoryctl: unexpected arguments")
		fs.Usage()
		return 2
	}
	if *factory == "" || *key == "" {
		if *factory == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		if *key == "" {
			say(stderr, "factoryctl: --key is required")
		}
		fs.Usage()
		return 2
	}
	c, err := clientFromEnv(getenv)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 2
	}
	req := client.InvocationRequest{
		SourceOccurrenceKey: *key,
		Intent:              *intent,
		Priority:            *priority,
	}
	if *profile != "" {
		req.Profile = &client.ProfileName{Name: *profile}
	}
	raw, status, err := c.Invoke(context.Background(), *factory, req)
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if status != http.StatusOK && status != http.StatusCreated {
		say(stderr, "factoryctl:", string(raw))
		return 1
	}
	var out json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	return 0
}
