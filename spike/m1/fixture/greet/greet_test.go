package greet

import "testing"

func TestHello(t *testing.T) {
	if got := Hello("M1"); got != "Hello, M1!" {
		t.Fatalf("Hello(%q) = %q, want %q", "M1", got, "Hello, M1!")
	}
}
