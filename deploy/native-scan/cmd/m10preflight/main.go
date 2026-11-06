package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	nativescan "github.com/polds/factory/deploy/native-scan"
)

func main() {
	mode := flag.String("mode", "", "offline or live")
	root := flag.String("root", "", "repository root")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "m10preflight: root is required")
		os.Exit(2)
	}
	now := time.Now().UTC()
	switch *mode {
	case "offline":
		if err := runOffline(*root, now); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "live":
		runLive(now)
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "usage: m10preflight --mode offline|live --root DIR")
		os.Exit(2)
	}
}

func runOffline(root string, now time.Time) error {
	pinOK, err := nativescan.PinOK(root)
	if err != nil {
		return err
	}
	if !pinOK {
		return fmt.Errorf("scanner pin is not plugin=stub fetch=forbidden")
	}
	var probes nativescan.ProbeCounter
	for _, item := range nativescan.OfflineCases(now, pinOK) {
		fmt.Printf("case %s\n", item.Name)
		checks := nativescan.RunPreflight(item.Witness, &probes)
		fmt.Print(nativescan.FormatChecks(checks))
		for _, name := range item.WantFail {
			if !nativescan.CheckFailed(checks, name) {
				return fmt.Errorf("case %s did not fail %s", item.Name, name)
			}
		}
		if extra := nativescan.UnexpectedFailures(checks, item.WantFail); len(extra) != 0 {
			return fmt.Errorf("case %s extra failures %v", item.Name, extra)
		}
	}
	fmt.Printf("model_probe count=%d\n", probes.Count())
	if probes.Count() != 0 {
		return fmt.Errorf("model probe ran")
	}
	fmt.Println("m10 preflight offline PASS")
	return nil
}

func runLive(now time.Time) {
	var probes nativescan.ProbeCounter
	checks := nativescan.RunPreflight(nativescan.LiveBlockedWitness(now), &probes)
	fmt.Println("case live_blocked")
	fmt.Print(nativescan.FormatChecks(checks))
	fmt.Printf("model_probe count=%d\n", probes.Count())
	if probes.Count() == 0 && nativescan.CheckFailed(checks, "backend_attestation_present") {
		fmt.Println("m10 preflight live blocked")
		return
	}
	fmt.Println("m10 preflight live unexpected")
}
