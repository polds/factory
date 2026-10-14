package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/polds/factory/internal/definition"
)

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, "usage: factoryctl validate PATH")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "factoryctl: validate requires PATH")
		fs.Usage()
		return 2
	}
	_, diags := definition.LoadDir(fs.Arg(0))
	code := 0
	for _, d := range diags {
		if _, err := fmt.Fprintf(stdout, "%s:%d: %s\n", d.File, d.Line, d.Message); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
		if d.Severity == definition.SeverityError {
			code = 1
		}
	}
	return code
}
