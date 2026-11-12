package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/polds/factory/internal/modelinfo"
)

// prepareSpend fills SpendPrice from the gateway when a spend cap is set and
// no explicit price was configured. It does not open a database or call a model.
func prepareSpend(ctx context.Context, cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.SpendPrice) != "" || cfg.SpendCapCents <= 0 {
		return cfg, nil
	}
	if strings.TrimSpace(cfg.InferenceBaseURL) == "" {
		return Config{}, errors.New("spend rates: inference base URL is required")
	}
	price, err := modelinfo.Derive(ctx, modelinfo.Request{
		BaseURL:  cfg.InferenceBaseURL,
		APIKey:   cfg.InferenceAPIKey,
		Extra:    cfg.InferenceExtraHeaders,
		Model:    cfg.InferenceModel,
		CapCents: cfg.SpendCapCents,
	})
	if err != nil {
		return Config{}, err
	}
	raw, err := json.Marshal(price)
	if err != nil {
		return Config{}, err
	}
	cfg.SpendPrice = string(raw)
	return cfg, nil
}
