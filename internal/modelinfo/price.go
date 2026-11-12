// Package modelinfo reads LiteLLM GET /model/info into a spend price.
// Rates come from the document. The package does not invent example rates.
package modelinfo

import (
	"encoding/json"
	"strings"

	"github.com/polds/factory/internal/harness"
)

const (
	// PromptByteCap and MaxTokens are the metering window W is computed against.
	PromptByteCap int64 = 400000
	MaxTokens     int64 = 8192
)

// PriceFromModelInfo reads a GET /model/info body for model and its fallbacks.
// Rates are USD per token in model_info (input_cost_per_token,
// output_cost_per_token, cache_creation_input_token_cost, above-N tiers).
// The fallback list is litellm_params.fallbacks. When that key is absent, the
// same list is read from router_settings.fallbacks, stored as
// [{model: [fallback, ...]}]. A missing list, a missing model, or a model
// with no input or output rate returns false.
func PriceFromModelInfo(raw []byte, model string) (harness.SpendPrice, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return harness.SpendPrice{}, false
	}
	var doc struct {
		Data []struct {
			ModelName string `json:"model_name"`
			Params    struct {
				Fallbacks []string `json:"fallbacks"`
			} `json:"litellm_params"`
			Info map[string]json.RawMessage `json:"model_info"`
		} `json:"data"`
		Router struct {
			Fallbacks []map[string][]string `json:"fallbacks"`
		} `json:"router_settings"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return harness.SpendPrice{}, false
	}
	byName := map[string]int{}
	for i, item := range doc.Data {
		byName[item.ModelName] = i
	}
	primary, ok := byName[model]
	if !ok {
		return harness.SpendPrice{}, false
	}
	fallbacks := doc.Data[primary].Params.Fallbacks
	if fallbacks == nil {
		fallbacks = routerFallbacks(doc.Router.Fallbacks, model)
	}
	if fallbacks == nil {
		return harness.SpendPrice{}, false
	}
	names := append([]string{model}, fallbacks...)
	price := harness.SpendPrice{PromptByteCap: PromptByteCap, MaxTokens: MaxTokens}
	for _, name := range names {
		idx, found := byName[name]
		if !found {
			return harness.SpendPrice{}, false
		}
		info := doc.Data[idx].Info
		rate := harness.ModelRate{Name: name}
		rate.InputCostPerToken = rawNumber(info["input_cost_per_token"])
		rate.OutputCostPerToken = rawNumber(info["output_cost_per_token"])
		rate.CacheCreationInputTokenCost = rawNumber(info["cache_creation_input_token_cost"])
		if rate.InputCostPerToken == "" || rate.OutputCostPerToken == "" {
			return harness.SpendPrice{}, false
		}
		for key, value := range info {
			if strings.Contains(key, "above") && strings.Contains(key, "input") {
				rate.InputAbove = append(rate.InputAbove, rawNumber(value))
			}
			if strings.Contains(key, "above") && strings.Contains(key, "output") {
				rate.OutputAbove = append(rate.OutputAbove, rawNumber(value))
			}
		}
		price.Models = append(price.Models, rate)
	}
	return price, true
}

func routerFallbacks(rows []map[string][]string, model string) []string {
	for _, row := range rows {
		if names, ok := row[model]; ok {
			return names
		}
	}
	return nil
}

func rawNumber(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	return strings.Trim(text, `"`)
}
