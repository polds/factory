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
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			t.Fatalf("id %q char %q", id, c)
		}
	}
}

func TestFormatDeliveryIDGolden(t *testing.T) {
	t.Parallel()
	zero := make([]byte, 10)
	maxEnt := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	got, err := formatDeliveryID(time.UnixMilli(0), zero)
	if err != nil || got != PrefixDelivery+"00000000000000000000000000" {
		t.Fatalf("zero %q %v", got, err)
	}
	got, err = formatDeliveryID(time.UnixMilli((1<<48)-1), maxEnt)
	if err != nil || got != PrefixDelivery+"7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max %q %v", got, err)
	}
}

func TestFormatDeliveryIDErrorText(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, 10)
	_, err := formatDeliveryID(time.UnixMilli((1<<48)-1).Add(time.Millisecond), entropy)
	if err == nil || err.Error() != "ingress: timestamp does not fit in 48 bits" {
		t.Fatalf("timestamp %v", err)
	}
	_, err = formatDeliveryID(time.UnixMilli(-1), entropy)
	if err == nil || err.Error() != "ingress: timestamp does not fit in 48 bits" {
		t.Fatalf("negative %v", err)
	}
	_, err = formatDeliveryID(time.UnixMilli(0), entropy[:9])
	if err == nil || err.Error() != "ingress: entropy must be 10 bytes" {
		t.Fatalf("entropy %v", err)
	}
}
