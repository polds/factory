package dreaminglive

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestKillSeatUser(t *testing.T) {
	if err := ensureSeatUser(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sudo", "-n", "-u", "seat", "--", "bash", "-c", "exec -a zz-seatkill sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = killSeatUser()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !seatLabelAlive("zz-seatkill") {
		time.Sleep(50 * time.Millisecond)
	}
	if !seatLabelAlive("zz-seatkill") {
		t.Fatal("seat process was not alive")
	}
	if err := killSeatUser(); err != nil {
		t.Fatal(err)
	}
	if seatLabelAlive("zz-seatkill") {
		t.Fatal("seat process survived pkill")
	}
}

func seatLabelAlive(label string) bool {
	out, err := exec.Command("ps", "-u", "seat", "-o", "args=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), label)
}
