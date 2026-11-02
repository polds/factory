package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTerminalPayloadCarriesPullURLs(t *testing.T) {
	raw := terminalPayload(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "succeeded", "", "", "", "", nil, []string{
		"https://pr.invalid/m7/factory-m7-target/pull/1",
	})
	var got struct {
		Type   string   `json:"type"`
		Status string   `json:"status"`
		PRURLs []string `json:"prUrls"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "run.terminal" || got.Status != "succeeded" || len(got.PRURLs) != 1 || got.PRURLs[0] != "https://pr.invalid/m7/factory-m7-target/pull/1" {
		t.Fatalf("payload %s", raw)
	}
	empty := terminalPayload(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "cancelled", "", "", "", "", nil, nil)
	var bare struct {
		PRURLs []string `json:"prUrls"`
	}
	if err := json.Unmarshal(empty, &bare); err != nil {
		t.Fatal(err)
	}
	if len(bare.PRURLs) != 0 {
		t.Fatalf("empty prUrls = %#v", bare.PRURLs)
	}
}

func TestPullURLsFromPayloadsAndWorkItem(t *testing.T) {
	blobs := [][]byte{
		[]byte(`{"type":"tool.result","prUrls":["https://pr.invalid/m7/factory-m7-target/pull/1"]}`),
		[]byte(`{"pr_urls":["https://github.com/acme/app/pull/2","https://pr.invalid/m7/factory-m7-target/pull/1"]}`),
		[]byte(`not json`),
	}
	got := mergePullURLs("https://github.com/acme/app/pull/2", pullURLsFromPayloads(blobs))
	if len(got) != 2 || got[0] != "https://github.com/acme/app/pull/2" || got[1] != "https://pr.invalid/m7/factory-m7-target/pull/1" {
		t.Fatalf("merged %#v", got)
	}
}
