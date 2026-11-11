package main

import (
	"context"
	"fmt"
	"os"

	"github.com/polds/factory/internal/harness"
)

func main() {
	env, err := harness.PrepareProcess(os.Args, os.Environ())
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory-harness: secret intake failed")
		os.Exit(harness.ExitEnvironment)
	}
	os.Exit(harness.Run(context.Background(), harness.Options{
		Args:   os.Args[1:],
		Env:    env,
		Stderr: os.Stderr,
	}))
}
