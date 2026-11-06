package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMintReadToken(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(30 * time.Minute)}
	c := newTestClient(t, key, fake, now)
	const sha = "0123456789abcdef0123456789abcdef01234567"
	ctx := context.Background()
	tok, err := c.MintReadToken(ctx, ReadMint{
		InstallationID: testInst,
		Repo:           "polds/factory",
		AllowedRepos:   []string{"polds/factory"},
		PinnedSHA:      sha,
		CheckoutSHA:    sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value() != testToken || tok.Repo() != "polds/factory" || tok.SHA() != sha {
		t.Fatal("mint result")
	}
	body := tokenBody(t, fake)
	want := ReadPermissions()
	if len(body.Permissions) != len(want) {
		t.Fatalf("permissions %#v", body.Permissions)
	}
	for name, level := range want {
		if body.Permissions[name] != level {
			t.Fatalf("permission %s=%s", name, level)
		}
	}
	for name, level := range body.Permissions {
		if level == "write" || name == "pull_requests" || name == "issues" || name == "checks" {
			t.Fatalf("write scope %s=%s", name, level)
		}
	}
	if err := tok.Push(); err == nil {
		t.Fatal("push was allowed")
	}
	if err := tok.CreatePullRequest(); err == nil {
		t.Fatal("pull request was allowed")
	}
	if err := tok.Comment(); err == nil {
		t.Fatal("comment was allowed")
	}
	printed := fmt.Sprintf("%v %+v %#v", tok, tok, &tok)
	if strings.Contains(printed, testToken) {
		t.Fatal("read token leaked")
	}
	if _, err := tok.MarshalJSON(); err == nil {
		t.Fatal("read token serialized")
	}

	if _, err := c.MintReadToken(ctx, ReadMint{
		InstallationID: testInst,
		Repo:           "other/repo",
		AllowedRepos:   []string{"polds/factory"},
		PinnedSHA:      sha,
		CheckoutSHA:    sha,
	}); err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("unrelated err %v", err)
	}
	if fake.mints != 1 {
		t.Fatalf("unrelated repo minted, mints %d", fake.mints)
	}
	if _, err := c.MintReadToken(ctx, ReadMint{
		InstallationID: testInst,
		Repo:           "polds/factory",
		AllowedRepos:   []string{"polds/factory"},
		PinnedSHA:      sha,
		CheckoutSHA:    strings.Repeat("ab", 20),
	}); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("checkout err %v", err)
	}
	if _, err := c.MintReadToken(ctx, ReadMint{
		InstallationID: testInst,
		Repo:           "polds/factory",
		AllowedRepos:   []string{"polds/factory"},
		PinnedSHA:      "main",
		CheckoutSHA:    "main",
	}); err == nil {
		t.Fatal("branch name was accepted as a pin")
	}
}
