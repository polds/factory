package main

import (
	"fmt"
	"os"

	"github.com/polds/factory/internal/harness/verify"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "harness" || os.Args[2] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: factoryctl harness verify --bin factory-harness")
		os.Exit(2)
	}
	bin := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--bin" && i+1 < len(args) {
			bin = args[i+1]
			i++
		}
	}
	if bin == "" {
		fmt.Fprintln(os.Stderr, "factoryctl: --bin is required")
		os.Exit(2)
	}
	if err := verify.Report(os.Stdout, verify.All(bin)); err != nil {
		os.Exit(1)
	}
}
