package harness

import (
	"net/url"
	"testing"
)

func TestRedactLongerSecretWins(t *testing.T) {
	got := Redact("xxabcdefghxx", []string{"abcd", "abcdefgh"})
	if got != "xx[REDACTED]xx" {
		t.Fatalf("%q", got)
	}
}

func TestRedactQueryAndJSONForms(t *testing.T) {
	secret := "abcd/efgh"
	escaped := url.QueryEscape(secret)
	got := Redact("pre "+escaped+" post", []string{secret})
	if got != "pre [REDACTED] post" {
		t.Fatalf("query: %q", got)
	}
	quoted := `aaaa"bbbb`
	got = Redact(`xxaaaa\"bbbbxx`, []string{quoted})
	if got != "xx[REDACTED]xx" {
		t.Fatalf("json: %q", got)
	}
}

func TestDigestIsSHA256OfOriginal(t *testing.T) {
	const input = "echo planted-secret-value"
	const want = "sha256:a4c33a6954a3b07c1161fa8360f2a84818f6307a91e168cc6d996d040f4a9c24"
	if got := Digest(input); got != want {
		t.Fatalf("got %s", got)
	}
}
