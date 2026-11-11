package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const (
	// EnvSpendURL is the run-token spend route, without the :reserve or :settle suffix.
	EnvSpendURL = "FACTORY_SPEND_URL"
	// EnvSpendPrice is the JSON price document the server also uses to compute W.
	EnvSpendPrice = "FACTORY_SPEND_PRICE"
	// EnvNoReexec skips the secret re-exec. The seat-isolation check uses it
	// as the failure case.
	EnvNoReexec = "FACTORY_HARNESS_NO_REEXEC"
	// EnvSecretsFD is the inherited pipe the re-exec'd process reads once.
	EnvSecretsFD = "FACTORY_SECRETS_FD"

	spendAttempt = "1"
)

// SpendPrice is the price document behind W.
// Rates are decimal USD per token. A model with a missing input or output
// rate makes W impossible to compute.
type SpendPrice struct {
	PromptByteCap int64       `json:"prompt_byte_cap"`
	MaxTokens     int64       `json:"max_tokens"`
	Models        []ModelRate `json:"models"`
}

// ModelRate is one primary or fallback. Empty optional rates are ignored.
// A set rate that does not parse fails the whole document.
type ModelRate struct {
	Name                        string   `json:"name"`
	InputCostPerToken           string   `json:"input_cost_per_token"`
	OutputCostPerToken          string   `json:"output_cost_per_token"`
	CacheCreationInputTokenCost string   `json:"cache_creation_input_token_cost,omitempty"`
	InputAbove                  []string `json:"input_above,omitempty"`
	OutputAbove                 []string `json:"output_above,omitempty"`
}

// ParseSpendPrice reads the price document. An empty document is an error.
func ParseSpendPrice(raw string) (SpendPrice, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SpendPrice{}, errors.New("spend price is empty")
	}
	var price SpendPrice
	if err := json.Unmarshal([]byte(raw), &price); err != nil {
		return SpendPrice{}, errors.New("spend price is not json")
	}
	return price, nil
}

// WorstCaseCents is W in cents:
// ceil(100 × ((prompt_byte_cap + 1024) × max input USD/token + max_tokens × max output USD/token)).
// The maxima cover every model, cache-creation, and above-N rate.
// ok is false when a rate is missing or W is not positive.
func (p SpendPrice) WorstCaseCents() (int64, bool) {
	if p.PromptByteCap < 0 || p.PromptByteCap > math.MaxInt64-1024 || p.MaxTokens <= 0 || len(p.Models) == 0 {
		return 0, false
	}
	var maxIn, maxOut *big.Rat
	for _, model := range p.Models {
		in, ok := decimalRate(model.InputCostPerToken)
		if !ok {
			return 0, false
		}
		out, ok := decimalRate(model.OutputCostPerToken)
		if !ok {
			return 0, false
		}
		maxIn = higherRate(maxIn, in)
		maxOut = higherRate(maxOut, out)
		if strings.TrimSpace(model.CacheCreationInputTokenCost) != "" {
			cache, ok := decimalRate(model.CacheCreationInputTokenCost)
			if !ok {
				return 0, false
			}
			maxIn = higherRate(maxIn, cache)
		}
		for _, extra := range model.InputAbove {
			rate, ok := decimalRate(extra)
			if !ok {
				return 0, false
			}
			maxIn = higherRate(maxIn, rate)
		}
		for _, extra := range model.OutputAbove {
			rate, ok := decimalRate(extra)
			if !ok {
				return 0, false
			}
			maxOut = higherRate(maxOut, rate)
		}
	}
	if maxIn == nil || maxOut == nil {
		return 0, false
	}
	bytes := new(big.Rat).SetInt64(p.PromptByteCap + 1024)
	inputCost := new(big.Rat).Mul(bytes, maxIn)
	tokens := new(big.Rat).SetInt64(p.MaxTokens)
	outputCost := new(big.Rat).Mul(tokens, maxOut)
	sum := inputCost.Add(inputCost, outputCost)
	w := ceilCents(sum)
	if w <= 0 {
		return 0, false
	}
	return w, true
}

func decimalRate(raw string) (*big.Rat, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	r := new(big.Rat)
	if _, ok := r.SetString(raw); !ok || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}

func higherRate(cur, next *big.Rat) *big.Rat {
	if cur == nil || next.Cmp(cur) > 0 {
		return next
	}
	return cur
}

func ceilCents(usd *big.Rat) int64 {
	scaled := new(big.Rat).Mul(usd, big.NewRat(100, 1))
	num := new(big.Int).Set(scaled.Num())
	den := scaled.Denom()
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0
	}
	return q.Int64()
}

// promptSize is the serialized messages plus the serialized tools.
func promptSize(messages []chatMessage, tools []any) (int, error) {
	msg, err := json.Marshal(messages)
	if err != nil {
		return 0, err
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return 0, err
	}
	return len(msg) + len(toolJSON), nil
}

func centsFromUSD(raw string) (int64, error) {
	rate, ok := decimalRate(raw)
	if !ok {
		return 0, errors.New("cost")
	}
	return ceilCents(rate), nil
}

func newSpendCallID(turn int) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "c" + hex.EncodeToString(buf[:]) + "-t" + fmt.Sprintf("%d", turn), nil
}

type spendSession struct {
	url       string
	token     string
	price     SpendPrice
	priced    bool
	w         int64
	maxTokens int
	byteCap   int64
	client    *http.Client
	stopped   bool
}

func newSpendSession(env []string) spendSession {
	url := strings.TrimSpace(lookupEnv(env, EnvSpendURL))
	if url == "" {
		return spendSession{}
	}
	session := spendSession{
		url:    url,
		token:  lookupEnv(env, "FACTORY_RUN_TOKEN"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
	price, err := ParseSpendPrice(lookupEnv(env, EnvSpendPrice))
	if err != nil {
		return session
	}
	session.price = price
	w, ok := price.WorstCaseCents()
	if !ok || price.MaxTokens > int64(math.MaxInt) {
		return session
	}
	session.priced = true
	session.w = w
	session.maxTokens = int(price.MaxTokens)
	session.byteCap = price.PromptByteCap
	return session
}

func (s spendSession) enabled() bool {
	return s.url != ""
}

type spendHold struct {
	id     string
	callID string
	cents  int64
}

type spendSettlement struct {
	Counted     int64
	Quarantined int64
	Paused      bool
}

func (s spendSession) reserve(ctx context.Context, callID string) (spendHold, bool, error) {
	body, err := json.Marshal(map[string]any{
		"cents":      s.w,
		"call_id":    callID,
		"attempt_id": spendAttempt,
	})
	if err != nil {
		return spendHold{}, false, err
	}
	status, raw, err := s.post(ctx, s.url+":reserve", body)
	if err != nil {
		return spendHold{}, false, err
	}
	if status != http.StatusOK {
		return spendHold{}, true, nil
	}
	var parsed struct {
		ReservationID string `json:"reservation_id"`
		Cents         int64  `json:"cents"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.ReservationID == "" || parsed.Cents <= 0 {
		return spendHold{}, true, nil
	}
	return spendHold{id: parsed.ReservationID, callID: callID, cents: parsed.Cents}, false, nil
}

func (s spendSession) settle(ctx context.Context, hold spendHold, cents int64, missing, allowance bool) (spendSettlement, error) {
	body, err := json.Marshal(map[string]any{
		"reservation_id":      hold.id,
		"call_id":             hold.callID,
		"attempt_id":          spendAttempt,
		"cents":               cents,
		"missing_usage":       missing,
		"allowance_exhausted": allowance,
	})
	if err != nil {
		return spendSettlement{}, err
	}
	status, raw, err := s.post(ctx, s.url+":settle", body)
	if err != nil {
		return spendSettlement{}, err
	}
	if status != http.StatusOK {
		return spendSettlement{}, errors.New("spend settle denied")
	}
	var parsed struct {
		Counted     int64 `json:"counted_cents"`
		Quarantined int64 `json:"quarantined_cents"`
		Paused      bool  `json:"paused"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return spendSettlement{}, errors.New("spend settle")
	}
	return spendSettlement{Counted: parsed.Counted, Quarantined: parsed.Quarantined, Paused: parsed.Paused}, nil
}

func (s spendSession) post(ctx context.Context, url string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errors.New("spend request")
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("spend request failed")
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, errors.New("spend response")
	}
	return res.StatusCode, raw, nil
}
