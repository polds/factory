package store

import (
	"strings"
	"testing"
)

func TestFormatIDKnownVectors(t *testing.T) {
	t.Parallel()
	zeroEntropy := make([]byte, 10)
	got, err := FormatID(PrefixRun, 0, zeroEntropy)
	if err != nil {
		t.Fatal(err)
	}
	if got != PrefixRun+"00000000000000000000000000" {
		t.Fatalf("zero id %q", got)
	}

	maxEntropy := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	got, err = FormatID(PrefixExec, (1<<48)-1, maxEntropy)
	if err != nil {
		t.Fatal(err)
	}
	if got != PrefixExec+"7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max id %q", got)
	}

	got, err = FormatID(PrefixFactory, 0x010203040506, zeroEntropy)
	if err != nil {
		t.Fatal(err)
	}
	if got != PrefixFactory+"01081G81860000000000000000" {
		t.Fatalf("timestamp id %q", got)
	}
}

func TestFormatIDSortsByTime(t *testing.T) {
	t.Parallel()
	entropy := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	earlier, err := FormatID(PrefixRun, 1_700_000_000_000, entropy)
	if err != nil {
		t.Fatal(err)
	}
	later, err := FormatID(PrefixRun, 1_700_000_000_001, entropy)
	if err != nil {
		t.Fatal(err)
	}
	if earlier >= later {
		t.Fatalf("time order %q >= %q", earlier, later)
	}
}

func TestFormatIDRejectsBadInput(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, 10)
	if _, err := FormatID(PrefixRun, 1<<48, entropy); err == nil {
		t.Fatal("accepted 49-bit timestamp")
	}
	if _, err := FormatID(PrefixRun, 1, entropy[:9]); err == nil {
		t.Fatal("accepted short entropy")
	}
	if _, err := FormatID("nope_", 1, entropy); err == nil {
		t.Fatal("accepted unknown prefix")
	}
}

func TestNewIDShapeAndUniqueness(t *testing.T) {
	t.Parallel()
	prefixes := []string{
		PrefixRun, PrefixExec, PrefixFactory, PrefixAgent, PrefixDefinition,
		PrefixWorkItem, PrefixApproval, PrefixConversation,
	}
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		id, err := NewID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		body, ok := strings.CutPrefix(id, prefix)
		if !ok {
			t.Fatalf("id %q missing prefix %s", id, prefix)
		}
		if len(body) != 26 {
			t.Fatalf("id %q body length %d", id, len(body))
		}
		for _, r := range body {
			if !strings.ContainsRune(crockfordAlphabet, r) {
				t.Fatalf("id %q has non-crockford rune %q", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	other, err := NewID(PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	if seen[other] {
		t.Fatalf("NewID repeated %s", other)
	}
	if _, err := NewID("run"); err == nil {
		t.Fatal("accepted prefix without trailing underscore")
	}
}
