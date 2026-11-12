package modelinfo

import "testing"

func TestDuplicateDeploymentUsesHigherRate(t *testing.T) {
	raw := []byte(`{
	  "data": [{
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]},
	    "model_info": {"input_cost_per_token": 0.000002, "output_cost_per_token": 0.000008, "cache_creation_input_token_cost": 0.000001}
	  }, {
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]},
	    "model_info": {"input_cost_per_token": 0.00003, "output_cost_per_token": 0.000004, "cache_creation_input_token_cost": 0.000004}
	  }, {
	    "model_name": "f",
	    "model_info": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000004}
	  }]
	}`)
	price, ok := PriceFromModelInfo(raw, "m")
	if !ok || len(price.Models) != 2 {
		t.Fatalf("ok=%v models=%d", ok, len(price.Models))
	}
	primary := price.Models[0]
	if primary.InputCostPerToken != "0.00003" || primary.OutputCostPerToken != "0.000008" || primary.CacheCreationInputTokenCost != "0.000004" {
		t.Fatalf("primary %#v", primary)
	}
}

func TestUnpricedSiblingDeploymentFails(t *testing.T) {
	raw := []byte(`{
	  "data": [{
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]},
	    "model_info": {"input_cost_per_token": null, "output_cost_per_token": null}
	  }, {
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]},
	    "model_info": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
	  }, {
	    "model_name": "f",
	    "model_info": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
	  }]
	}`)
	if _, ok := PriceFromModelInfo(raw, "m"); ok {
		t.Fatal("null sibling was priced")
	}
	raw = []byte(`{
	  "data": [{
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]}
	  }, {
	    "model_name": "m",
	    "litellm_params": {"fallbacks": ["f"]},
	    "model_info": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
	  }, {
	    "model_name": "f",
	    "model_info": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
	  }]
	}`)
	if _, ok := PriceFromModelInfo(raw, "m"); ok {
		t.Fatal("deployment without model_info was priced")
	}
}

func TestExplicitEmptyFallbackListSkipsRouter(t *testing.T) {
	raw := []byte(`{
	  "router_settings": {"fallbacks": [{"m": ["f"]}]},
	  "data": [{
	    "model_name": "m",
	    "litellm_params": {"fallbacks": []},
	    "model_info": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000004}
	  }, {
	    "model_name": "f",
	    "model_info": {"input_cost_per_token": 0.00003, "output_cost_per_token": 0.00015}
	  }]
	}`)
	price, ok := PriceFromModelInfo(raw, "m")
	if !ok || len(price.Models) != 1 || price.Models[0].Name != "m" {
		t.Fatalf("ok=%v models=%v", ok, price.Models)
	}
}
