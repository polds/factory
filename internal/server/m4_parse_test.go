package server

import "testing"

func TestGitRepo(t *testing.T) {
	cases := []struct {
		raw   string
		owner string
		name  string
	}{
		{raw: "https://github.com/acme/factory.git", owner: "acme", name: "factory"},
		{raw: "git@github.com:acme/factory.git", owner: "acme", name: "factory"},
		{raw: "https://github.com/acme/factory", owner: "acme", name: "factory"},
	}
	for _, tc := range cases {
		owner, name, ok := gitRepo(tc.raw)
		if !ok || owner != tc.owner || name != tc.name {
			t.Fatalf("%s -> %s/%s ok %v", tc.raw, owner, name, ok)
		}
	}
	if _, _, ok := gitRepo("https://example.invalid/only"); ok {
		t.Fatal("single path segment matched")
	}
}

func TestGitHubPullURL(t *testing.T) {
	want := "https://github.com/acme/payments-service/pull/77"
	cases := []struct {
		raw  string
		want string
	}{
		{raw: want, want: want},
		{raw: `{"text":"` + want + `"}`, want: want},
		{raw: "see " + want + "/files for the diff", want: want},
		{raw: "https://github.com.evil.com/acme/payments-service/pull/77", want: ""},
		{raw: "https://notgithub.com/acme/payments-service/pull/77", want: ""},
		{raw: "https://user:pass@github.com/acme/payments-service/pull/77", want: ""},
		{raw: "https://evil.example/https://github.com/acme/payments-service/pull/77", want: want},
	}
	for _, tc := range cases {
		if got := githubPullURL([]byte(tc.raw)); got != tc.want {
			t.Fatalf("%s -> %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestIssueMentionsAndRef(t *testing.T) {
	got := issueMentions("Closes #412 and also #7")
	if len(got) != 2 || got[0] != 412 || got[1] != 7 {
		t.Fatalf("%v", got)
	}
	repo, n, ok := parseIssueRef("acme/payments-service#412")
	if !ok || repo != "acme/payments-service" || n != 412 {
		t.Fatalf("%s %d %v", repo, n, ok)
	}
}
