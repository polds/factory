package operator

import "testing"

func TestPushGeneration(t *testing.T) {
	if got := pushGeneration(1, 0, true); got != 1 {
		t.Fatalf("first publish %d", got)
	}
	if got := pushGeneration(1, 1, true); got != 2 {
		t.Fatalf("child change %d", got)
	}
	if got := pushGeneration(3, 1, true); got != 3 {
		t.Fatalf("factory generation %d", got)
	}
	if got := pushGeneration(1, 2, false); got != 2 {
		t.Fatalf("unchanged resend %d", got)
	}
}
