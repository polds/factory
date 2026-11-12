package modelinfo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/inference"
)

// modelInfoClient bounds a hung gateway so a missing rate fails closed.
var modelInfoClient = &http.Client{Timeout: 30 * time.Second}

// Request is one gateway rate lookup for a configured model.
type Request struct {
	BaseURL  string
	APIKey   string
	Extra    []inference.Header
	Model    string
	CapCents int64
}

// Derive fetches GET {BaseURL}/model/info and prices Model plus its fallbacks.
// A transport error, a non-2xx response, a document that does not price every
// model in the chain, or a worst case above CapCents is an error.
func Derive(ctx context.Context, req Request) (harness.SpendPrice, error) {
	if req.CapCents <= 0 {
		return harness.SpendPrice{}, errors.New("spend rates: cap is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		return harness.SpendPrice{}, errors.New("spend rates: model is required")
	}
	raw, err := fetchModelInfo(ctx, req)
	if err != nil {
		return harness.SpendPrice{}, err
	}
	price, ok := PriceFromModelInfo(raw, req.Model)
	if !ok {
		return harness.SpendPrice{}, errors.New("spend rates: model info has no rates for the model and its fallbacks")
	}
	cents, priced := price.WorstCaseCents()
	if !priced || cents <= 0 {
		return harness.SpendPrice{}, errors.New("spend rates: worst case is not priced")
	}
	if cents > req.CapCents {
		return harness.SpendPrice{}, fmt.Errorf("spend rates: W=%d exceeds cap %d", cents, req.CapCents)
	}
	return price, nil
}

func fetchModelInfo(ctx context.Context, req Request) ([]byte, error) {
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		return nil, errors.New("spend rates: inference base URL is required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/model/info", nil)
	if err != nil {
		return nil, errors.New("spend rates: model info request")
	}
	// The gateway key travels in x-litellm-api-key. Extra headers replace that
	// default by name. The key is not placed in the URL.
	inference.Apply(httpReq.Header, "", req.Extra)
	if key := strings.TrimSpace(req.APIKey); key != "" && httpReq.Header.Get("x-litellm-api-key") == "" {
		httpReq.Header.Set("x-litellm-api-key", key)
	}
	res, err := modelInfoClient.Do(httpReq)
	if err != nil {
		return nil, errors.New("spend rates: model info request failed")
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, errors.New("spend rates: model info request failed")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("spend rates: model info HTTP %d", res.StatusCode)
	}
	return body, nil
}
