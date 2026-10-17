package failure

import "fmt"

// signalNames maps a signal number to the name Evidence prints.
// 128+n is the worker's normalized exit (docs/04, docs/05).
var signalNames = map[int]string{
	1:  "SIGHUP",
	2:  "SIGINT",
	3:  "SIGQUIT",
	4:  "SIGILL",
	5:  "SIGTRAP",
	6:  "SIGABRT",
	7:  "SIGBUS",
	8:  "SIGFPE",
	9:  "SIGKILL",
	10: "SIGUSR1",
	11: "SIGSEGV",
	12: "SIGUSR2",
	13: "SIGPIPE",
	14: "SIGALRM",
	15: "SIGTERM",
	16: "SIGSTKFLT",
	17: "SIGCHLD",
	18: "SIGCONT",
	19: "SIGSTOP",
	20: "SIGTSTP",
	21: "SIGTTIN",
	22: "SIGTTOU",
	23: "SIGURG",
	24: "SIGXCPU",
	25: "SIGXFSZ",
	26: "SIGVTALRM",
	27: "SIGPROF",
	28: "SIGWINCH",
	29: "SIGIO",
	30: "SIGPWR",
	31: "SIGSYS",
}

func signalName(sig int) string {
	return signalNames[sig]
}

func formatExit(code int) string {
	if code < 128 {
		return fmt.Sprintf("exit code %d", code)
	}
	sig := code - 128
	if name := signalName(sig); name != "" {
		return fmt.Sprintf("exit code %d (%s)", code, name)
	}
	if sig > 0 {
		return fmt.Sprintf("exit code %d (signal %d)", code, sig)
	}
	return fmt.Sprintf("exit code %d", code)
}

func killSummary(code int) string {
	sig := code - 128
	if name := signalName(sig); name != "" {
		return fmt.Sprintf("Container was killed by %s (exit %d)", name, code)
	}
	if sig > 0 {
		return fmt.Sprintf("Container was killed by signal %d (exit %d)", sig, code)
	}
	return fmt.Sprintf("Container was killed (exit %d)", code)
}
