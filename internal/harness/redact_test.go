package harness

import (
	"net/url"
	"strings"
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

func TestRedactionNeedlesLiveShape(t *testing.T) {
	const repo = "https://github.com/polds/factory-m7-target"
	const api = "https://api.github.com"
	token := "ghs_" + strings.Repeat("a", 20)
	pr := repo + "/pull/1"
	cred := "https://x-access-token:" + token + "@github.com/polds/factory-m7-target.git"
	encodedPW := "ghs%5F" + strings.Repeat("a", 20)
	encoded := "https://x-access-token:" + encodedPW + "@github.com/polds/factory-m7-target.git"
	query := repo + "?access_token=" + token
	opaque := repo + "/" + strings.Repeat("p", 24)
	splice := repo + "\n" + token

	needles := RedactionNeedles([]string{repo, api, token, cred})
	for _, dropped := range []string{repo, repo + ".git", pr, api, api + "/"} {
		if containsNeedle(needles, dropped) {
			t.Fatalf("public locator %q is a needle in %q", dropped, needles)
		}
	}
	if !containsNeedle(needles, token) || !containsNeedle(needles, cred) {
		t.Fatalf("credential missing from %q", needles)
	}
	got := Redact(pr+" "+token, needles)
	if got != pr+" [REDACTED]" {
		t.Fatalf("pull url: %q", got)
	}
	if strings.Contains(got, "[REDACTED]/pull") {
		t.Fatalf("pull url was prefixed: %q", got)
	}
	bare := Redact("token "+token+" left", RedactionNeedles([]string{cred}))
	if strings.Contains(bare, token) || bare != "token [REDACTED] left" {
		t.Fatalf("credential url did not mask the token: %q", bare)
	}
	enc := Redact(encodedPW+" "+("ghs_"+strings.Repeat("a", 20)), RedactionNeedles([]string{encoded}))
	if strings.Contains(enc, encodedPW) || strings.Contains(enc, "ghs_"+strings.Repeat("a", 20)) {
		t.Fatalf("encoded userinfo password survived: %q", enc)
	}
	for _, secret := range []string{query, opaque, splice, "http://m7-forge.factory.svc.cluster.local:8080"} {
		if publicHTTPSURL(secret) {
			t.Fatalf("%q was treated as public", secret)
		}
		masked := Redact(secret, RedactionNeedles([]string{secret}))
		if masked != "[REDACTED]" {
			t.Fatalf("%q redacted to %q", secret, masked)
		}
	}
	if !publicHTTPSURL(api+"\n") || !publicHTTPSURL(repo+".git") || !publicHTTPSURL(pr) {
		t.Fatal("public locators were rejected")
	}
}

func TestSecretListDropsPublicRepoURL(t *testing.T) {
	repo := "https://github.com/polds/factory-m7-target"
	token := "ghs_" + strings.Repeat("a", 20)
	got := secretList(TaskFile{Secrets: []string{repo}}, []string{
		"FACTORY_SECRET_VALUES=" + repo + "\n" + token + "\nhttps://api.github.com",
	})
	if containsNeedle(got, repo) || containsNeedle(got, "https://api.github.com") {
		t.Fatalf("public urls stayed in the harness list: %q", got)
	}
	pr := repo + "/pull/1"
	red := Redact(pr+" "+token, got)
	if red != pr+" [REDACTED]" {
		t.Fatalf("%q", red)
	}
}

func containsNeedle(needles []string, want string) bool {
	for _, needle := range needles {
		if needle == want {
			return true
		}
	}
	return false
}

func TestDigestIsSHA256OfOriginal(t *testing.T) {
	const input = "echo planted-secret-value"
	const want = "sha256:a4c33a6954a3b07c1161fa8360f2a84818f6307a91e168cc6d996d040f4a9c24"
	if got := Digest(input); got != want {
		t.Fatalf("got %s", got)
	}
}
