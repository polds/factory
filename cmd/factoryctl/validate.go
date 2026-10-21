package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/polds/factory/internal/definition"
)

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base definition directory for guard rails")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl validate [--base DIR] PATH")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "factoryctl: validate requires PATH")
		fs.Usage()
		return 2
	}
	if strings.TrimSpace(*base) == "" {
		_, diags := definition.LoadDir(fs.Arg(0))
		return writeDiagnostics(diags, stdout, stderr)
	}
	return validateGuardRails(strings.TrimSpace(*base), fs.Arg(0), stdout, stderr)
}

func validateGuardRails(basePath, headPath string, stdout, stderr io.Writer) int {
	baseTree, baseDiags := definition.LoadDir(basePath)
	headTree, headDiags := definition.LoadDir(headPath)
	code := writeDiagnostics(errorDiagnostics(baseDiags), stdout, stderr)
	if c := writeDiagnostics(errorDiagnostics(headDiags), stdout, stderr); c != 0 {
		code = c
	}
	if baseTree == nil || headTree == nil {
		if code == 0 {
			return 1
		}
		return code
	}
	if c := writeDiagnostics(definition.GuardRails(baseTree, headTree), stdout, stderr); c != 0 {
		code = c
	}
	return code
}

func errorDiagnostics(diags []definition.Diagnostic) []definition.Diagnostic {
	var out []definition.Diagnostic
	for _, d := range diags {
		if d.Severity == definition.SeverityError {
			out = append(out, d)
		}
	}
	return out
}

func writeDiagnostics(diags []definition.Diagnostic, stdout, stderr io.Writer) int {
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
