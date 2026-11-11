package main

import (
	"context"
	"fmt"
	"os"

	dreaminglive "github.com/polds/factory/deploy/dreaming-live"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dreamingpreflight offline|preflight|live")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "offline":
		if err := dreaminglive.RunOffline(context.Background(), "preflight.json"); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fmt.Println("dreaming offline PASS")
	case "preflight", "live":
		if err := dreaminglive.RunChecked(context.Background(), os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: dreamingpreflight offline|preflight|live")
		os.Exit(2)
	}
}
