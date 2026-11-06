package main

import (
	"fmt"
	"os"

	nativescan "github.com/polds/factory/deploy/native-scan"
	"github.com/polds/factory/internal/outbound"
)

func main() {
	dir, err := os.MkdirTemp("", "m10-accept-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "m10accept: temp dir")
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	_, sum, err := nativescan.RunAcceptance(dir, outbound.NewBridge())
	if err != nil {
		fmt.Fprintln(os.Stderr, "m10accept: acceptance failed")
		os.Exit(1)
	}
	fmt.Print(nativescan.FormatSummary(sum))
}
