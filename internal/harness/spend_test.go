package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorstCaseCentsUnits(t *testing.T) {
	worked := SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     8192,
		Models: []ModelRate{
			{
				Name:                        "nous/claude-sonnet-5.5",
				InputCostPerToken:           "0.000003",
				OutputCostPerToken:          "0.000015",
				CacheCreationInputTokenCost: "0.00000375",
			},
			{Name: "fallback", InputCostPerToken: "0.000003", OutputCostPerToken: "0.000015"},
		},
	}
	w, ok := worked.WorstCaseCents()
	if !ok || w != 163 {
		t.Fatalf("W = %d ok=%v, want 163", w, ok)
	}
	over := SpendPrice{
		PromptByteCap: 398976,
		MaxTokens:     8000,
		Models:        []ModelRate{{Name: "high", InputCostPerToken: "0.00006", OutputCostPerToken: "0.00025"}},
	}
	w, ok = over.WorstCaseCents()
	if !ok || w != 2600 {
		t.Fatalf("W = %d ok=%v, want 2600", w, ok)
	}
	one := SpendPrice{
		PromptByteCap: 1,
		MaxTokens:     1,
		Models:        []ModelRate{{Name: "tiny", InputCostPerToken: "0.00000001", OutputCostPerToken: "0.00000001"}},
	}
	w, ok = one.WorstCaseCents()
	if !ok || w != 1 {
		t.Fatalf("W = %d ok=%v, want 1", w, ok)
	}
	missing := worked
	missing.Models = []ModelRate{{Name: "fallback", InputCostPerToken: "0.000003"}}
	if _, ok := missing.WorstCaseCents(); ok {
		t.Fatal("unpriced fallback computed W")
	}
	cents, err := centsFromUSD("1.62672")
	if err != nil || cents != 163 {
		t.Fatalf("cents %d %v", cents, err)
	}
	cents, err = centsFromUSD("0.015")
	if err != nil || cents != 2 {
		t.Fatalf("round up %d %v", cents, err)
	}
}

func TestRunWithoutSpendURLOmitsMaxTokens(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-api-key-value",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=1",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: t.TempDir(),
		Stderr:     io.Discard,
	})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(string(body), "max_tokens") {
		t.Fatal("unset spend sent max_tokens")
	}
}

func TestEvidenceRunDoesNotReserve(t *testing.T) {
	var calls int
	spend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "reserved", http.StatusInternalServerError)
	}))
	t.Cleanup(spend.Close)
	rt := t.TempDir()
	task := TaskFile{EvidencePackage: ".", EvidenceStatus: EvidencePrivilegeUnavailable}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1"},
		Env: []string{
			"OPENAI_BASE_URL=http://127.0.0.1:1",
			"OPENAI_API_KEY=sk-api-key-value",
			EnvSpendURL + "=" + spend.URL + "/spend",
			EnvSpendPrice + `={"prompt_byte_cap":400000,"max_tokens":8192,"models":[{"name":"m","input_cost_per_token":"0.000003","output_cost_per_token":"0.000015"}]}`,
			"FACTORY_MAX_TURNS=1",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: rt,
		Stderr:     io.Discard,
	})
	if code == ExitOK {
		t.Fatal("evidence privilege failure exited 0")
	}
	if calls != 0 {
		t.Fatalf("evidence reserved %d", calls)
	}
}

func TestScrubEnvDropsSpend(t *testing.T) {
	out := scrubEnv([]string{
		EnvSpendURL + "=http://spend.example/secret",
		EnvSpendPrice + `={"models":[]}`,
		"FOO=bar",
	}, toolEnvDeny(nil))
	if len(out) != 1 || out[0] != "FOO=bar" {
		t.Fatalf("scrub %v", out)
	}
}
