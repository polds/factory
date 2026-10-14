package main

import (
	"io"

	"github.com/polds/factory/internal/harness/verify"
)

func cmdHarness(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "verify" {
		say(stderr, "usage: factoryctl harness verify --bin factory-harness")
		return 2
	}
	bin := ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--bin" && i+1 < len(rest) {
			bin = rest[i+1]
			i++
		}
	}
	if bin == "" {
		say(stderr, "factoryctl: --bin is required")
		return 2
	}
	if err := verify.Report(stdout, verify.All(bin)); err != nil {
		return 1
	}
	return 0
}
