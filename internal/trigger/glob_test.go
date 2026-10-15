package trigger

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{pattern: "a.go", name: "a.go", want: true},
		{pattern: "a.go", name: "A.go", want: false},
		{pattern: "*.go", name: "a.go", want: true},
		{pattern: "*.go", name: "dir/a.go", want: false},
		{pattern: "**/*.go", name: "a.go", want: true},
		{pattern: "**/*.go", name: "dir/a.go", want: true},
		{pattern: "**/*.go", name: "dir/a.txt", want: false},
		{pattern: "services/payments/**", name: "services/payments/api/pay.go", want: true},
		{pattern: "services/payments/**", name: "services/payments", want: true},
		{pattern: "services/payments/**", name: "services/other/a.go", want: false},
		{pattern: "a/**/b", name: "a/b", want: true},
		{pattern: "a/**/b", name: "a/x/y/b", want: true},
		{pattern: "?", name: "a", want: true},
		{pattern: "?", name: "ab", want: false},
		{pattern: "a?c", name: "abc", want: true},
		{pattern: "[abc]", name: "b", want: true},
		{pattern: "[abc]", name: "d", want: false},
		{pattern: "[!a]", name: "b", want: true},
		{pattern: "[!a]", name: "a", want: false},
		{pattern: "[^a]", name: "b", want: true},
		{pattern: "[a-c]", name: "b", want: true},
		{pattern: "[a-c]", name: "d", want: false},
		{pattern: "[", name: "a", want: false},
		{pattern: `a\*/b`, name: "a*/b", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"→"+tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.name); got != tc.want {
				t.Fatalf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
			}
		})
	}
}
