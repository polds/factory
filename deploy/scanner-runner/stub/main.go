// Command scanner-stub is the public scanner plugin stand-in.
// It does not fetch plugin source.
package main

import (
	"fmt"
	"os"
)

func main() {
	if _, err := fmt.Fprintln(os.Stdout, "scanner-stub plugin=stub fetch=forbidden"); err != nil {
		os.Exit(1)
	}
}
