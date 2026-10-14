package handoff

import (
	"strings"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestFormatIDMatchesRunScheme(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ms      uint64
		entropy []byte
	}{
		{ms: 0, entropy: make([]byte, 10)},
		{ms: (1 << 48) - 1, entropy: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{ms: 0x010203040506, entropy: make([]byte, 10)},
	}
	for _, tc := range cases {
		runID, err := store.FormatID(store.PrefixRun, tc.ms, tc.entropy)
		if err != nil {
			t.Fatal(err)
		}
		convID, err := formatID(PrefixConversation, tc.ms, tc.entropy)
		if err != nil {
			t.Fatal(err)
		}
		runBody := strings.TrimPrefix(runID, store.PrefixRun)
		convBody := strings.TrimPrefix(convID, PrefixConversation)
		if runBody == runID || convBody == convID || runBody != convBody {
			t.Fatalf("scheme mismatch run %q conv %q", runID, convID)
		}
		if !strings.HasPrefix(convID, "conv_") {
			t.Fatalf("prefix %q", convID)
		}
	}
}

func TestFormatIDRejectsBadInput(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, 10)
	if _, err := formatID(PrefixConversation, 1<<48, entropy); err == nil {
		t.Fatal("accepted 49-bit timestamp")
	}
	if _, err := formatID(PrefixConversation, 1, entropy[:9]); err == nil {
		t.Fatal("accepted short entropy")
	}
	if _, err := formatID("run_", 1, entropy); err == nil {
		t.Fatal("accepted run prefix")
	}
}

func TestNewConversationIDShapeAndUniqueness(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		id := NewConversationID()
		body, ok := strings.CutPrefix(id, PrefixConversation)
		if !ok {
			t.Fatalf("id %q missing prefix", id)
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
}
