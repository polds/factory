package nativescan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	wantCodeSHA       = "1111111111111111111111111111111111111111"
	wantImageDigest   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wantAdapterDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	wantCapability    = "scanner"
	wantGrant         = "contents:read"
	attestationMaxAge = 24 * time.Hour
)

// Check is one independent preflight result.
type Check struct {
	Name string
	Pass bool
}

// Witness is the no-network preflight input. CredentialName is a name, never a secret value.
type Witness struct {
	AttestationPresent bool
	AttestationAt      time.Time
	Now                time.Time
	CodeSHA            string
	ImageDigest        string
	AdapterDigest      string
	Capability         string
	CredentialName     string
	Grant              string
	ExecuteTarget      bool
	MountsSecure       bool
	PrivateImageURL    string
	PinOK              bool
	G14Actual          bool
	G18Actual          bool
	GatewayCapProven   bool
	HostFirewall       bool
	CNIProved          bool
	Backend            string
}

// ProbeCounter counts model-probe attempts. Probe does not dial.
type ProbeCounter struct {
	n int
}

// Probe records one attempt.
func (p *ProbeCounter) Probe() {
	if p != nil {
		p.n++
	}
}

// Count is the number of attempts.
func (p *ProbeCounter) Count() int {
	if p == nil {
		return 0
	}
	return p.n
}

// PaidProbeAllowed is the actual-backend formula. Synthetic witnesses stay false.
func PaidProbeAllowed(w Witness) bool {
	if !w.G14Actual || !w.G18Actual || !w.GatewayCapProven {
		return false
	}
	if w.Backend == "docker" {
		return w.HostFirewall
	}
	if w.Backend == "k8s" {
		return w.CNIProved
	}
	return false
}

// Evaluate runs every independent check. It does not probe a model.
func Evaluate(w Witness) []Check {
	fresh := w.AttestationPresent && !w.AttestationAt.IsZero() && w.Now.Sub(w.AttestationAt) <= attestationMaxAge && !w.AttestationAt.After(w.Now)
	return []Check{
		{Name: "backend_attestation_present", Pass: w.AttestationPresent},
		{Name: "backend_attestation_fresh", Pass: fresh},
		{Name: "code_sha", Pass: w.CodeSHA != "" && w.CodeSHA == wantCodeSHA},
		{Name: "image_digest", Pass: w.ImageDigest != "" && w.ImageDigest == wantImageDigest},
		{Name: "adapter_digest", Pass: w.AdapterDigest != "" && w.AdapterDigest == wantAdapterDigest},
		{Name: "capability", Pass: w.Capability != "" && w.Capability == wantCapability},
		{Name: "credential", Pass: w.CredentialName != ""},
		{Name: "grant", Pass: w.Grant != "" && w.Grant == wantGrant},
		{Name: "execute_target_disabled", Pass: !w.ExecuteTarget},
		{Name: "mounts_secure", Pass: w.MountsSecure},
		{Name: "private_image_fetch", Pass: w.PrivateImageURL == "" && w.PinOK},
		{Name: "paid_probe_closed", Pass: !PaidProbeAllowed(w)},
	}
}

// MayProbe is true only when the paid-probe formula holds and every other check passed.
// paid_probe_closed is the closed-gate signal, so an open formula does not have to pass it.
func MayProbe(w Witness, checks []Check) bool {
	if !PaidProbeAllowed(w) {
		return false
	}
	for _, check := range checks {
		if check.Name == "paid_probe_closed" {
			continue
		}
		if !check.Pass {
			return false
		}
	}
	return true
}

// RunPreflight evaluates the witness and probes only when MayProbe is true.
func RunPreflight(w Witness, probes *ProbeCounter) []Check {
	checks := Evaluate(w)
	if MayProbe(w, checks) {
		probes.Probe()
	}
	return checks
}

// SecureWitness is the synthetic offline profile. It is not an actual-backend attestation.
func SecureWitness(now time.Time, pinOK bool) Witness {
	return Witness{
		AttestationPresent: true,
		AttestationAt:      now.Add(-time.Minute),
		Now:                now,
		CodeSHA:            wantCodeSHA,
		ImageDigest:        wantImageDigest,
		AdapterDigest:      wantAdapterDigest,
		Capability:         wantCapability,
		CredentialName:     "SCAN_GITHUB_TOKEN",
		Grant:              wantGrant,
		ExecuteTarget:      false,
		MountsSecure:       true,
		PinOK:              pinOK,
		Backend:            "docker",
	}
}

// PinOK reads the public scanner pin. Private fetch stays forbidden.
func PinOK(root string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "scanner-runner", "pin", "provenance"))
	if err != nil {
		return false, err
	}
	text := string(raw)
	return strings.Contains(text, "plugin=stub") && strings.Contains(text, "fetch=forbidden"), nil
}

// ModuleRoot walks parents until go.mod.
func ModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

// Case is one named preflight witness.
type Case struct {
	Name     string
	Witness  Witness
	WantFail []string
}

// OfflineCases is the no-network refusal matrix plus the synthetic secure profile.
func OfflineCases(now time.Time, pinOK bool) []Case {
	secure := SecureWitness(now, pinOK)
	absent := secure
	absent.AttestationPresent = false
	stale := secure
	stale.AttestationAt = now.Add(-48 * time.Hour)
	wrongSHA := secure
	wrongSHA.CodeSHA = "0000000000000000000000000000000000000000"
	wrongImage := secure
	wrongImage.ImageDigest = strings.Repeat("c", 64)
	wrongAdapter := secure
	wrongAdapter.AdapterDigest = strings.Repeat("d", 64)
	missingCap := secure
	missingCap.Capability = ""
	missingCred := secure
	missingCred.CredentialName = ""
	missingGrant := secure
	missingGrant.Grant = ""
	execute := secure
	execute.ExecuteTarget = true
	mounts := secure
	mounts.MountsSecure = false
	privateURL := secure
	privateURL.PrivateImageURL = "https://example.invalid/scanner.git"
	return []Case{
		{Name: "absent_attestation", Witness: absent, WantFail: []string{"backend_attestation_present", "backend_attestation_fresh"}},
		{Name: "stale_attestation", Witness: stale, WantFail: []string{"backend_attestation_fresh"}},
		{Name: "wrong_sha", Witness: wrongSHA, WantFail: []string{"code_sha"}},
		{Name: "wrong_image", Witness: wrongImage, WantFail: []string{"image_digest"}},
		{Name: "wrong_adapter", Witness: wrongAdapter, WantFail: []string{"adapter_digest"}},
		{Name: "missing_capability", Witness: missingCap, WantFail: []string{"capability"}},
		{Name: "missing_credential", Witness: missingCred, WantFail: []string{"credential"}},
		{Name: "missing_grant", Witness: missingGrant, WantFail: []string{"grant"}},
		{Name: "execute_target", Witness: execute, WantFail: []string{"execute_target_disabled"}},
		{Name: "insecure_mounts", Witness: mounts, WantFail: []string{"mounts_secure"}},
		{Name: "private_image", Witness: privateURL, WantFail: []string{"private_image_fetch"}},
		{Name: "secure_synthetic", Witness: secure, WantFail: nil},
	}
}

// LiveBlockedWitness refuses the live gate. It is not a passed attestation.
func LiveBlockedWitness(now time.Time) Witness {
	return Witness{
		Now:             now,
		ExecuteTarget:   true,
		MountsSecure:    false,
		PrivateImageURL: "https://example.invalid/scanner.git",
		PinOK:           false,
		Backend:         "docker",
	}
}

// FormatChecks renders PASS/FAIL lines.
func FormatChecks(checks []Check) string {
	var b strings.Builder
	for _, check := range checks {
		state := "FAIL"
		if check.Pass {
			state = "PASS"
		}
		fmt.Fprintf(&b, "%s %s\n", state, check.Name)
	}
	return b.String()
}

// CheckFailed reports whether name failed.
func CheckFailed(checks []Check, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return !check.Pass
		}
	}
	return false
}

// UnexpectedFailures returns failed checks that were not listed.
func UnexpectedFailures(checks []Check, want []string) []string {
	wantSet := map[string]struct{}{}
	for _, name := range want {
		wantSet[name] = struct{}{}
	}
	var extra []string
	for _, check := range checks {
		if check.Pass {
			continue
		}
		if _, ok := wantSet[check.Name]; !ok {
			extra = append(extra, check.Name)
		}
	}
	return extra
}
