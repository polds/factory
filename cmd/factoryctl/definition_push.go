package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/convert"
)

func cmdDefinitionPush(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("definition push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	factoryName := fs.String("factory", "", "factory name")
	fs.Usage = func() {
		say(stderr, "usage: factoryctl definition push --factory NAME DIR")
	}
	flagArgs, pos, err := reorderArgs(args, helpFlags)
	if err != nil {
		say(stderr, "factoryctl:", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *factoryName == "" || len(pos) != 1 {
		if *factoryName == "" {
			say(stderr, "factoryctl: --factory is required")
		}
		fs.Usage()
		return 2
	}
	files, err := convert.ReadFiles(pos[0])
	if err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	if len(files) == 0 {
		say(stderr, "factoryctl: no definition files")
		return 1
	}
	req := api.DefinitionPushRequest{Generation: 0}
	for _, file := range files {
		req.Files = append(req.Files, api.DefinitionFile{Path: file.Path, Content: file.Content})
	}
	c, code := clientOrExit(stderr, getenv)
	if code != 0 {
		return code
	}
	res, err := c.PushDefinition(context.Background(), *factoryName, req)
	if err != nil {
		if errors.Is(err, client.ErrGitBackedFactory) {
			say(stderr, "factoryctl: factory is git-backed")
			return 1
		}
		say(stderr, "factoryctl:", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "valid: %t\npublished_generation: %d\n", res.Valid, res.PublishedGeneration); err != nil {
		say(stderr, "factoryctl:", err)
		return 1
	}
	for _, diag := range res.Errors {
		if _, err := fmt.Fprintf(stdout, "%s:%d: %s\n", diag.File, diag.Line, diag.Message); err != nil {
			say(stderr, "factoryctl:", err)
			return 1
		}
	}
	if !res.Valid {
		return 1
	}
	return 0
}
