package main

import (
	"context"
	"flag"
	"io"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/polds/factory/internal/convert"
)

func cmdCRDs(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		crdsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "print":
		if len(args) != 1 {
			crdsUsage(stderr)
			return 2
		}
		raw, err := convert.CRDYAML()
		if err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if _, err := stdout.Write(raw); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		return 0
	case "apply":
		fs := flag.NewFlagSet("crds apply", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dryRun := fs.Bool("dry-run", false, "server-side dry run")
		fs.Usage = func() { crdsUsage(stderr) }
		flagArgs, pos, err := reorderArgs(args[1:], map[string]bool{
			"dry-run": true, "h": true, "help": true,
		})
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
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if err := convert.ApplyCRDs(context.Background(), cfg, *dryRun); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		return 0
	default:
		crdsUsage(stderr)
		return 2
	}
}

func crdsUsage(w io.Writer) {
	say(w, "usage: factoryctl crds print")
	say(w, "       factoryctl crds apply [--dry-run]")
}
