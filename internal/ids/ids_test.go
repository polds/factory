package ids

import (
	"strings"
	"testing"
)

func TestFormatGolden(t *testing.T) {
	t.Parallel()
	zero := make([]byte, 10)
	maxEnt := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	prefixes := []string{
		"run_", "exec_", "fac_", "agt_", "def_", "task_", "apr_", "conv_",
		"dlv_", "sch_", "inr_", "whk_", "str_", "aut_", "scr_", "ibt_",
		"bmr_", "bmt_",
	}
	for _, prefix := range prefixes {
		got, err := Format(prefix, 0, zero)
		if err != nil {
			t.Fatal(err)
		}
		if got != prefix+"00000000000000000000000000" {
			t.Fatalf("zero %q", got)
		}
		got, err = Format(prefix, (1<<48)-1, maxEnt)
		if err != nil {
			t.Fatal(err)
		}
		if got != prefix+"7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
			t.Fatalf("max %q", got)
		}
	}
}

func TestFormatErrorText(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, 10)
	_, err := Format("run_", 1<<48, entropy)
	if err == nil || err.Error() != "timestamp does not fit in 48 bits" {
		t.Fatalf("timestamp %v", err)
	}
	_, err = Format("run_", 1, entropy[:9])
	if err == nil || err.Error() != "entropy must be 10 bytes" {
		t.Fatalf("entropy %v", err)
	}
}

func TestNewShape(t *testing.T) {
	t.Parallel()
	id, err := New("run_")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := strings.CutPrefix(id, "run_")
	if !ok || len(body) != 26 {
		t.Fatalf("id %q", id)
	}
	for _, r := range body {
		if !strings.ContainsRune(crockfordAlphabet, r) {
			t.Fatalf("id %q rune %q", id, r)
		}
	}
}
