package harness

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSecretPipeRoundTrip(t *testing.T) {
	wire := secretWire{
		APIKey:       "sk-pipe-key",
		RunToken:     "run-pipe-token",
		SpendURL:     "http://spend.example/runs/1/spend",
		SecretValues: "sk-pipe-key\nrun-pipe-token",
		ExtraHeaders: "x-test: sk-pipe-key",
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dup, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFD(strconv.Itoa(dup))
	if err != nil {
		t.Fatal(err)
	}
	if got != wire {
		t.Fatalf("wire %+v", got)
	}
	restored := restoreSecrets([]string{"PATH=/usr/bin", EnvSecretsFD + "=9", "OPENAI_API_KEY=old"}, got)
	joined := strings.Join(restored, "\n")
	if strings.Contains(joined, "OPENAI_API_KEY=old") || strings.Contains(joined, EnvSecretsFD+"=") {
		t.Fatal("stale secret env survived")
	}
	if !strings.Contains(joined, "OPENAI_API_KEY=sk-pipe-key") || !strings.Contains(joined, "FACTORY_RUN_TOKEN=run-pipe-token") {
		t.Fatal("restored env dropped a secret")
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatal("restored env dropped PATH")
	}
}
