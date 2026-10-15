package outbound

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBackoffDoublesAndCapsAtFiveMinutes(t *testing.T) {
	want := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		64 * time.Second,
		128 * time.Second,
		256 * time.Second,
	}
	for i, d := range want {
		if got := backoff(i); got != d {
			t.Fatalf("backoff(%d) = %s, want %s", i, got, d)
		}
		if got := backoff(i); got > backoffCap {
			t.Fatalf("backoff(%d) = %s exceeds %s", i, got, backoffCap)
		}
	}
	if got := backoff(9); got != backoffCap {
		t.Fatalf("backoff(9) = %s, want %s", got, backoffCap)
	}
	if got := backoff(30); got != backoffCap {
		t.Fatalf("backoff(30) = %s, want %s", got, backoffCap)
	}
}

func TestRedactStripsTokensAndKeys(t *testing.T) {
	err := errors.New("slack down xoxb-CANARY-DO-NOT-LOG and ghs_CANARY plus\n-----BEGIN RSA PRIVATE KEY-----\nsecret\n-----END RSA PRIVATE KEY-----")
	got := redact(err)
	for _, secret := range []string{"xoxb-CANARY-DO-NOT-LOG", "ghs_CANARY", "BEGIN RSA PRIVATE KEY", "secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redact kept %q in %s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redact = %s", got)
	}
	if redact(nil) != "" {
		t.Fatal("nil error")
	}
}
