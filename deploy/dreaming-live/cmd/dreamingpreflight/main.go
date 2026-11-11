package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	dreaminglive "github.com/polds/factory/deploy/dreaming-live"
)

func main() {
	if err := dreaminglive.ReexecCleanDriver(); err != nil {
		fmt.Fprintln(os.Stderr, "driver re-exec failed")
		os.Exit(1)
	}
	if err := loadModelKey(); err != nil {
		fmt.Fprintln(os.Stderr, "model key")
		os.Exit(1)
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dreamingpreflight offline|preflight|live|chain|publish")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "offline":
		if err := dreaminglive.RunOffline(context.Background(), "preflight.json"); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fmt.Println("dreaming offline PASS")
	case "preflight", "live", "chain":
		if err := dreaminglive.RunChecked(context.Background(), os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "publish":
		dir := strings.TrimSpace(os.Getenv("FACTORY_CHAIN_OUT"))
		if dir == "" {
			fmt.Fprintln(os.Stderr, "FACTORY_CHAIN_OUT is required")
			os.Exit(2)
		}
		if err := dreaminglive.PublishFromDir(context.Background(), dir); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: dreamingpreflight offline|preflight|live|chain|publish")
		os.Exit(2)
	}
}

func loadModelKey() error {
	fd := strings.TrimSpace(os.Getenv("FACTORY_MODEL_KEY_FD"))
	if fd == "" {
		return nil
	}
	n, err := strconv.Atoi(fd)
	if err != nil || n < 0 {
		return err
	}
	file := os.NewFile(uintptr(n), "model-key")
	if file == nil {
		return os.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, 1<<20))
	_ = file.Close()
	if err != nil {
		return err
	}
	dreaminglive.SetModelKey(strings.TrimSpace(string(body)))
	return nil
}
