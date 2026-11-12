package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polds/factory/internal/harness"
)

func TestPrepareSpendUsesGatewayRates(t *testing.T) {
	const (
		primaryIn    = "0.000001"
		primaryOut   = "0.000004"
		primaryCache = "0.0000012"
		fallbackIn   = "0.0000015"
		fallbackOut  = "0.000005"
	)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/model/info" {
			http.Error(w, "model call", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("x-litellm-api-key") != "rate-key" {
			t.Errorf("x-litellm-api-key %q", r.Header.Get("x-litellm-api-key"))
		}
		_, _ = w.Write([]byte(`{
		  "data": [{
		    "model_name": "metered-primary",
		    "litellm_params": {"fallbacks": ["metered-fallback"]},
		    "model_info": {
		      "input_cost_per_token": ` + primaryIn + `,
		      "output_cost_per_token": ` + primaryOut + `,
		      "cache_creation_input_token_cost": ` + primaryCache + `
		    }
		  }, {
		    "model_name": "metered-fallback",
		    "model_info": {
		      "input_cost_per_token": ` + fallbackIn + `,
		      "output_cost_per_token": ` + fallbackOut + `
		    }
		  }]
		}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := prepareSpend(context.Background(), gatewayCfg(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := harness.ParseSpendPrice(cfg.SpendPrice)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Models) != 2 {
		t.Fatalf("models %#v", parsed.Models)
	}
	if parsed.Models[0].Name != "metered-primary" ||
		parsed.Models[0].InputCostPerToken != primaryIn ||
		parsed.Models[0].OutputCostPerToken != primaryOut ||
		parsed.Models[0].CacheCreationInputTokenCost != primaryCache {
		t.Fatalf("primary %#v", parsed.Models[0])
	}
	if parsed.Models[1].Name != "metered-fallback" ||
		parsed.Models[1].InputCostPerToken != fallbackIn ||
		parsed.Models[1].OutputCostPerToken != fallbackOut {
		t.Fatalf("fallback %#v", parsed.Models[1])
	}
	if parsed.Models[0].InputCostPerToken == "0.000003" || parsed.Models[0].OutputCostPerToken == "0.000015" {
		t.Fatal("metered the example rates")
	}
	cents, ok := parsed.WorstCaseCents()
	if !ok || cents <= 0 || cents > 250 {
		t.Fatalf("W=%d ok=%v", cents, ok)
	}
	for _, path := range paths {
		if strings.Contains(path, "chat/completions") {
			t.Fatalf("model call %s", path)
		}
	}
}

func TestOpenSpendRatesFailBeforeRun(t *testing.T) {
	valid := `{
	  "data": [{
	    "model_name": "metered-primary",
	    "litellm_params": {"fallbacks": ["metered-fallback"]},
	    "model_info": {"input_cost_per_token": 0.00002, "output_cost_per_token": 0.00008}
	  }, {
	    "model_name": "metered-fallback",
	    "model_info": {"input_cost_per_token": 0.00002, "output_cost_per_token": 0.00008}
	  }]
	}`
	missing := `{
	  "data": [{
	    "model_name": "metered-primary",
	    "litellm_params": {"fallbacks": ["metered-fallback"]},
	    "model_info": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000004}
	  }, {
	    "model_name": "metered-fallback",
	    "model_info": {"input_cost_per_token": 0.000001}
	  }]
	}`
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 500", status: http.StatusInternalServerError, body: valid},
		{name: "malformed", status: http.StatusOK, body: `{"data":`},
		{name: "missing fallback rate", status: http.StatusOK, body: missing},
		{name: "W over cap", status: http.StatusOK, body: valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var chats int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "chat/completions") || r.URL.Path != "/model/info" {
					chats++
					http.Error(w, "model call", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			cfg := gatewayCfg(t, srv.URL)
			cfg.DatabaseURL = "postgres://127.0.0.1:1/factory?sslmode=disable"
			cfg.APIKey = "k"
			cfg.WorkerAPIKey = "w"
			cfg.DefinitionPath = t.TempDir()
			cfg.Console = "off"
			_, err := Open(context.Background(), cfg)
			if err == nil {
				t.Fatal("expected spend rates error")
			}
			if !strings.Contains(err.Error(), "spend rates") {
				t.Fatalf("reached run creation: %v", err)
			}
			if strings.Contains(err.Error(), "rate-key") {
				t.Fatalf("error leaked the key: %v", err)
			}
			if chats != 0 {
				t.Fatalf("model calls %d", chats)
			}
		})
	}
}

func TestPrepareSpendKeepsExplicitPrice(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cfg := gatewayCfg(t, srv.URL)
	cfg.SpendPrice = `{"prompt_byte_cap":1,"max_tokens":1,"models":[{"name":"set","input_cost_per_token":"0.00000001","output_cost_per_token":"0.00000001"}]}`
	got, err := prepareSpend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpendPrice != cfg.SpendPrice {
		t.Fatalf("price %s", got.SpendPrice)
	}
	if hits != 0 {
		t.Fatalf("gateway hits %d", hits)
	}
}

func TestPrepareSpendSkipsWithoutCap(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)
	cfg := gatewayCfg(t, srv.URL)
	cfg.SpendCapCents = 0
	got, err := prepareSpend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpendPrice != "" || hits != 0 {
		t.Fatalf("price %q hits %d", got.SpendPrice, hits)
	}
}

func gatewayCfg(t *testing.T, base string) Config {
	t.Helper()
	return Config{
		InferenceBaseURL: base,
		InferenceAPIKey:  "rate-key",
		InferenceModel:   "metered-primary",
		SpendCapCents:    250,
	}
}
