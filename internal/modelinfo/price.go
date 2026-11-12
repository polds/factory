// Package modelinfo reads LiteLLM GET /model/info into a spend price.
// Rates come from the document. The package does not invent example rates.
package modelinfo

import (
	"encoding/json"
	"math/big"
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
// The fallback list is litellm_params.fallbacks. When that key is absent on
// every deployment of the model, the same list is read from
// router_settings.fallbacks, stored as [{model: [fallback, ...]}]. A missing
// list, a missing model, or a model with no input or output rate returns false.
// Several data entries may share a model_name, one per deployment. Each rate
// field is the maximum across those entries, so W covers the deployment the
// router can actually select.
func PriceFromModelInfo(raw []byte, model string) (harness.SpendPrice, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return harness.SpendPrice{}, false
	}
	var doc struct {
		Data   []infoEntry `json:"data"`
		Router struct {
			Fallbacks []map[string][]string `json:"fallbacks"`
		} `json:"router_settings"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return harness.SpendPrice{}, false
	}
	byName := map[string][]int{}
	for i, item := range doc.Data {
		if strings.TrimSpace(item.ModelName) == "" {
			continue
		}
		byName[item.ModelName] = append(byName[item.ModelName], i)
	}
	idxs, ok := byName[model]
	if !ok {
		return harness.SpendPrice{}, false
	}
	fallbacks, set := deploymentFallbacks(doc.Data, idxs)
	if !set {
		fallbacks = routerFallbacks(doc.Router.Fallbacks, model)
	}
	if fallbacks == nil {
		return harness.SpendPrice{}, false
	}
	names := append([]string{model}, fallbacks...)
	price := harness.SpendPrice{PromptByteCap: PromptByteCap, MaxTokens: MaxTokens}
	for _, name := range names {
		group, found := byName[name]
		if !found {
			return harness.SpendPrice{}, false
		}
		rate, priced := rateFor(name, doc.Data, group)
		if !priced {
			return harness.SpendPrice{}, false
		}
		price.Models = append(price.Models, rate)
	}
	return price, true
}

type infoEntry struct {
	ModelName string `json:"model_name"`
	Params    struct {
		Fallbacks []string `json:"fallbacks"`
	} `json:"litellm_params"`
	Info map[string]json.RawMessage `json:"model_info"`
}

func deploymentFallbacks(data []infoEntry, idxs []int) ([]string, bool) {
	var out []string
	seen := map[string]struct{}{}
	set := false
	for _, i := range idxs {
		list := data[i].Params.Fallbacks
		if list == nil {
			continue
		}
		set = true
		for _, name := range list {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	if !set {
		return nil, false
	}
	if out == nil {
		out = []string{}
	}
	return out, true
}

func rateFor(name string, data []infoEntry, idxs []int) (harness.ModelRate, bool) {
	var infos []map[string]json.RawMessage
	for _, i := range idxs {
		infos = append(infos, data[i].Info)
	}
	in, ok := requiredMaxField(infos, "input_cost_per_token")
	if !ok {
		return harness.ModelRate{}, false
	}
	out, ok := requiredMaxField(infos, "output_cost_per_token")
	if !ok {
		return harness.ModelRate{}, false
	}
	cache, ok := maxField(infos, "cache_creation_input_token_cost")
	if !ok {
		return harness.ModelRate{}, false
	}
	aboveIn, ok := maxAbove(infos, true)
	if !ok {
		return harness.ModelRate{}, false
	}
	aboveOut, ok := maxAbove(infos, false)
	if !ok {
		return harness.ModelRate{}, false
	}
	return harness.ModelRate{
		Name:                        name,
		InputCostPerToken:           in,
		OutputCostPerToken:          out,
		CacheCreationInputTokenCost: cache,
		InputAbove:                  aboveIn,
		OutputAbove:                 aboveOut,
	}, true
}

// requiredMaxField is the maximum of key across every deployment.
// A missing, null, or unparseable value on any deployment fails closed.
func requiredMaxField(infos []map[string]json.RawMessage, key string) (string, bool) {
	if len(infos) == 0 {
		return "", false
	}
	var best string
	var bestRate *big.Rat
	for _, info := range infos {
		raw, ok := info[key]
		if !ok {
			return "", false
		}
		text := rawNumber(raw)
		if text == "" || text == "null" {
			return "", false
		}
		rate, ok := parseRate(text)
		if !ok {
			return "", false
		}
		if bestRate == nil || rate.Cmp(bestRate) > 0 {
			best = text
			bestRate = rate
		}
	}
	return best, best != ""
}

func maxField(infos []map[string]json.RawMessage, key string) (string, bool) {
	var best string
	var bestRate *big.Rat
	seen := false
	for _, info := range infos {
		raw, ok := info[key]
		if !ok || string(raw) == "null" {
			continue
		}
		text := rawNumber(raw)
		if text == "" {
			continue
		}
		rate, ok := parseRate(text)
		if !ok {
			return "", false
		}
		if !seen || rate.Cmp(bestRate) > 0 {
			best = text
			bestRate = rate
			seen = true
		}
	}
	return best, true
}

func maxAbove(infos []map[string]json.RawMessage, input bool) ([]string, bool) {
	keys := map[string]struct{}{}
	for _, info := range infos {
		for key := range info {
			if !strings.Contains(key, "above") {
				continue
			}
			if input && strings.Contains(key, "input") {
				keys[key] = struct{}{}
			}
			if !input && strings.Contains(key, "output") {
				keys[key] = struct{}{}
			}
		}
	}
	var out []string
	for key := range keys {
		text, ok := maxField(infos, key)
		if !ok || text == "" {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

func parseRate(raw string) (*big.Rat, bool) {
	rate := new(big.Rat)
	if _, ok := rate.SetString(strings.TrimSpace(raw)); !ok || rate.Sign() < 0 {
		return nil, false
	}
	return rate, true
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
