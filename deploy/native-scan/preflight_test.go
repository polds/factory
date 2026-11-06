package nativescan

import (
	"strings"
	"testing"
	"time"
)

func TestM10PreflightRefusals(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	root, err := ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	pinOK, err := PinOK(root)
	if err != nil || !pinOK {
		t.Fatalf("pin %v ok=%v", err, pinOK)
	}
	var probes ProbeCounter
	for _, item := range OfflineCases(now, pinOK) {
		checks := RunPreflight(item.Witness, &probes)
		if len(checks) != 12 {
			t.Fatalf("%s checks %d", item.Name, len(checks))
		}
		for _, name := range item.WantFail {
			if !CheckFailed(checks, name) {
				t.Fatalf("%s did not fail %s\n%s", item.Name, name, FormatChecks(checks))
			}
		}
		if extra := UnexpectedFailures(checks, item.WantFail); len(extra) != 0 {
			t.Fatalf("%s extra failures %v\n%s", item.Name, extra, FormatChecks(checks))
		}
		if PaidProbeAllowed(item.Witness) {
			t.Fatalf("%s paid probe allowed", item.Name)
		}
	}
	if probes.Count() != 0 {
		t.Fatalf("model probes %d", probes.Count())
	}
	live := RunPreflight(LiveBlockedWitness(now), &probes)
	if !CheckFailed(live, "backend_attestation_present") || probes.Count() != 0 {
		t.Fatalf("live probes %d\n%s", probes.Count(), FormatChecks(live))
	}
	if !strings.Contains(FormatChecks(live), "FAIL backend_attestation_present") {
		t.Fatal(FormatChecks(live))
	}
}

func TestM10FormulaProbeCounter(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	w := SecureWitness(now, true)
	w.G14Actual = true
	w.G18Actual = true
	w.GatewayCapProven = true
	w.HostFirewall = true
	w.Backend = "docker"
	if !PaidProbeAllowed(w) {
		t.Fatal("formula")
	}
	var probes ProbeCounter
	checks := RunPreflight(w, &probes)
	if probes.Count() != 1 {
		t.Fatalf("probes %d", probes.Count())
	}
	if !CheckFailed(checks, "paid_probe_closed") {
		t.Fatal("open formula still looked closed")
	}
	w.HostFirewall = false
	checks = RunPreflight(w, &probes)
	if PaidProbeAllowed(w) || probes.Count() != 1 || !CheckPassed(checks, "paid_probe_closed") {
		t.Fatalf("closed formula probes %d", probes.Count())
	}
}

func CheckPassed(checks []Check, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return check.Pass
		}
	}
	return false
}
