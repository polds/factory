package ingress

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDeliveryID(t *testing.T) {
	t.Parallel()
	got, err := formatDeliveryID(time.UnixMilli(0), make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	if got != PrefixDelivery+"00000000000000000000000000" {
		t.Fatalf("zero id %q", got)
	}
	entropy := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	earlier, err := formatDeliveryID(time.UnixMilli(1_700_000_000_000), entropy)
	if err != nil {
		t.Fatal(err)
	}
	later, err := formatDeliveryID(time.UnixMilli(1_700_000_000_001), entropy)
	if err != nil {
		t.Fatal(err)
	}
	if earlier >= later {
		t.Fatalf("time order %q >= %q", earlier, later)
	}
	if _, err := formatDeliveryID(time.UnixMilli(0), entropy[:9]); err == nil {
		t.Fatal("accepted short entropy")
	}
}

func TestNewDeliveryIDShape(t *testing.T) {
	t.Parallel()
	id, err := newDeliveryID(time.UnixMilli(1_700_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := strings.CutPrefix(id, PrefixDelivery)
	if !ok || len(body) != 26 {
		t.Fatalf("id %q", id)
	}
	for _, c := range body {
		if !strings.ContainsRune(crockfordAlphabet, c) {
			t.Fatalf("id %q char %q", id, c)
		}
	}
}
