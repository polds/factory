package main

import (
	"context"
	"os"

	"github.com/polds/factory/internal/harness"
)

func main() {
	os.Exit(harness.Run(context.Background(), harness.Options{
		Args:   os.Args[1:],
		Env:    os.Environ(),
		Stderr: os.Stderr,
	}))
}
