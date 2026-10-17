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
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(stdout)
		return 0
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
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "task":
		return cmdTask(args[1:], stdout, stderr, getenv)
	case "approvals":
		return cmdApprovals(args[1:], stdout, stderr, getenv)
	case "deliveries":
		return cmdDeliveries(args[1:], stdout, stderr, getenv)
	case "schedules":
		return cmdSchedules(args[1:], stdout, stderr, getenv)
	case "runs":
		return cmdRuns(args[1:], stdout, stderr, getenv)
	case "inbox":
		return cmdInbox(args[1:], stdout, stderr, getenv)
	case "workers":
		return cmdWorkers(args[1:], stdout, stderr, getenv)
	case "automations":
		return cmdAutomations(args[1:], stdout, stderr, getenv)
	case "secrets":
		return cmdSecrets(args[1:], stdout, stderr, getenv)
	case "definition":
		return cmdDefinition(args[1:], stdout, stderr, getenv)
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
  validate PATH
  task create --factory NAME --title TEXT [--body TEXT] [--source issue|manual|api] [--ref REF]
  task list --factory NAME [--stage STAGE] [--status STATUS]
  task get --factory NAME TASK_ID
  task cancel --factory NAME TASK_ID
  task watch --factory NAME TASK_ID
  task answer TASK_ID --text TEXT
  approvals list --factory NAME
  approvals decide APPROVAL_ID --decision approve|reject [--note TEXT]
  deliveries list --factory NAME [--unmatched]
  deliveries replay DELIVERY_ID
  schedules list [--factory NAME]
  schedules pause SCHEDULE_ID
  schedules resume SCHEDULE_ID
  runs show RUN_ID
  runs interrupt RUN_ID
  runs followup RUN_ID --text TEXT
  inbox --factory NAME
  workers
  automations list --factory NAME
  automations pause --factory NAME AUTOMATION
  automations resume --factory NAME AUTOMATION
  secrets list --factory NAME
  definition show --factory NAME

Environment:
  FACTORY_URL       control plane base URL (default http://localhost:8080)
  FACTORY_API_KEY   bearer token for the API
`)
}
