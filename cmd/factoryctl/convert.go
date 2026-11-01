package main

import (
	"context"
	"flag"
	"io"
	"os"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/polds/factory/internal/convert"
)

func cmdConvert(args []string, stdout, stderr io.Writer) int {
	return cmdConvertReader(args, stdout, stderr, os.Stdin)
}

func cmdConvertReader(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fromManifests := fs.String("from-manifests", "", "manifest file, or - for stdin")
	fromCluster := fs.Bool("from-cluster", false, "read definition kinds from the cluster")
	namespace := fs.String("namespace", "", "namespace for --from-cluster")
	fs.StringVar(namespace, "n", "", "namespace for --from-cluster")
	instructions := fs.Bool("instructions-configmap", false, "emit instruction bodies as ConfigMaps")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl convert DIR [--instructions-configmap]")
		say(stderr, "       factoryctl convert --from-manifests FILE|- DIR")
		say(stderr, "       factoryctl convert --from-cluster -n NS DIR")
	}
	flagArgs, pos, err := reorderArgs(args, map[string]bool{
		"from-cluster": true, "instructions-configmap": true, "h": true, "help": true,
	})
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	modes := 0
	if *fromManifests != "" {
		modes++
	}
	if *fromCluster {
		modes++
	}
	if modes > 1 {
		say(stderr, "factoryctl: choose one of DIR, --from-manifests, or --from-cluster")
		fs.Usage()
		return 2
	}
	switch {
	case *fromManifests != "":
		if len(pos) != 1 {
			say(stderr, "factoryctl: convert --from-manifests requires DIR")
			fs.Usage()
			return 2
		}
		var raw []byte
		if *fromManifests == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(*fromManifests)
		}
		if err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if err := convert.ManifestsToDir(raw, pos[0]); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		return 0
	case *fromCluster:
		if len(pos) != 1 || strings.TrimSpace(*namespace) == "" {
			say(stderr, "factoryctl: convert --from-cluster requires -n NS and DIR")
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
		if err := convert.FromCluster(context.Background(), cfg, *namespace, pos[0]); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		return 0
	default:
		if len(pos) != 1 {
			say(stderr, "factoryctl: convert requires DIR")
			fs.Usage()
			return 2
		}
		raw, err := convert.DirToManifests(pos[0], convert.Options{InstructionsConfigMap: *instructions})
		if err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if _, err := stdout.Write(raw); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		return 0
	}
}
