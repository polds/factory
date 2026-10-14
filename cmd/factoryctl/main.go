package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func execute(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "harness":
		return cmdHarness(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr, getenv)
	case "watch":
		return cmdWatch(args[1:], stdout, stderr, getenv)
	case "cancel":
		return cmdCancel(args[1:], stdout, stderr, getenv)
	default:
		usage(stderr)
		return 2
	}
}

func say(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: factoryctl <command>

Commands:
  harness verify --bin factory-harness
  run --prompt TEXT [--factory NAME] [--agent NAME] [--title TITLE] [--watch]
  watch RUN_ID [--from-seq N]
  cancel RUN_ID

Environment:
  FACTORY_URL       control plane base URL (default http://localhost:8080)
  FACTORY_API_KEY   bearer token for the API
`)
}
