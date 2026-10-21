package ingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/polds/factory/internal/event"
)

var (
	// ErrTooLarge is returned when a request body is larger than MaxBody.
	// Callers answer 413. The text is not written to the response or a log.
	ErrTooLarge          = errors.New("ingress: body exceeds 1MiB")
	errMissingDeliveryID = errors.New("ingress: missing delivery id")
	errUnknownProvider   = errors.New("ingress: unknown provider")
)

// Config builds an ingress handler.
// Now, when set, is the clock passed to the verifier and stored on the
// delivery. Metrics, when set, receives the non-persisted reject counts.
type Config struct {
	Provider         string
	Verifier         Verifier
	Sink             Sink
	Now              func() time.Time
	Metrics          *Metrics
	WebhookUID       string
	DeliveryIDHeader string

	// parser reads the Slack envelope. Nil uses encoding/json.
	// Tests install a spy. The handler calls it only after verify succeeds.
	parser slackParser
}

// Handler returns the ingress handler for provider.
// It does not look up or call a normalizer.
func Handler(provider string, v Verifier, sink Sink) http.Handler {
	return New(Config{Provider: provider, Verifier: v, Sink: sink})
}

// New returns an ingress handler.
func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &handler{
		provider:       cfg.Provider,
		verifier:       cfg.Verifier,
		sink:           cfg.Sink,
		now:            cfg.Now,
		metrics:        cfg.Metrics,
		webhookUID:     cfg.WebhookUID,
		deliveryHeader: cfg.DeliveryIDHeader,
		parser:         cfg.parser,
	}
}

type handler struct {
	provider       string
	verifier       Verifier
	sink           Sink
	now            func() time.Time
	metrics        *Metrics
	webhookUID     string
	deliveryHeader string
	parser         slackParser
}

type slackEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	EventID   string `json:"event_id"`
}

type slackParser interface {
	Parse(body []byte) (slackEnvelope, error)
}

type stdSlackParser struct{}

func (stdSlackParser) Parse(body []byte) (slackEnvelope, error) {
	var env slackEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return slackEnvelope{}, err
	}
	return env, nil
}

type ackBody struct {
	Duplicate bool `json:"duplicate"`
}

type challengeBody struct {
	Challenge string `json:"challenge"`
}

type errorBody struct {
	Error string `json:"error"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: "method not allowed"})
		return
	}
	if h.verifier == nil || h.sink == nil || h.provider == "" {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}

	body, err := ReadBody(r)
	if errors.Is(err, ErrTooLarge) {
		h.metrics.addTooLarge()
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "payload too large"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
		return
	}

	now := h.now()
	if err := h.verifier.Verify(r.Header, body, now); err != nil {
		h.metrics.addVerifyFailed()
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthorized"})
		return
	}

	var slack slackEnvelope
	if h.provider == event.ProviderSlack {
		slack, err = h.parseSlack(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
			return
		}
		if slack.Type == "url_verification" {
			if slack.Challenge == "" {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
				return
			}
			writeJSON(w, http.StatusOK, challengeBody{Challenge: slack.Challenge})
			return
		}
	}

	deliveryID, err := h.deliveryID(r.Header, slack)
	if errors.Is(err, errUnknownProvider) {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
		return
	}

	id, err := newDeliveryID(now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	stored, truncated := ClipBody(body)
	dup, err := h.sink.Enqueue(r.Context(), Delivery{
		ID:         id,
		Provider:   h.provider,
		WebhookUID: h.webhookUID,
		DeliveryID: deliveryID,
		ReceivedAt: now.UTC(),
		Headers:    RedactHeaders(r.Header),
		Body:       stored,
		Truncated:  truncated,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, ackBody{Duplicate: dup})
}

func (h *handler) parseSlack(body []byte) (slackEnvelope, error) {
	p := h.parser
	if p == nil {
		p = stdSlackParser{}
	}
	return p.Parse(body)
}

func (h *handler) deliveryID(header http.Header, slack slackEnvelope) (string, error) {
	if h.deliveryHeader != "" && h.provider != event.ProviderSlack {
		return oneHeader(header, h.deliveryHeader)
	}
	switch h.provider {
	case event.ProviderGitHub:
		return oneHeader(header, "X-GitHub-Delivery")
	case event.ProviderLinear:
		return oneHeader(header, "Linear-Delivery")
	case event.ProviderSlack:
		if slack.EventID == "" {
			return "", errMissingDeliveryID
		}
		return slack.EventID, nil
	case event.ProviderWebhook:
		if h.deliveryHeader == "" {
			return "", errMissingDeliveryID
		}
		return oneHeader(header, h.deliveryHeader)
	default:
		return "", fmt.Errorf("%w: %s", errUnknownProvider, h.provider)
	}
}

func oneHeader(h http.Header, name string) (string, error) {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return "", errMissingDeliveryID
	}
	return v, nil
}

// ReadBody reads at most MaxBody bytes. A larger body or Content-Length
// returns ErrTooLarge. A nil body is an empty payload. The body is closed.
func ReadBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > MaxBody {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, ErrTooLarge
	}
	if r.Body == nil {
		return []byte{}, nil
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBody {
		return nil, ErrTooLarge
	}
	return body, nil
}

// ClipBody returns at most MaxStoredBody bytes and reports whether body was longer.
func ClipBody(body []byte) ([]byte, bool) {
	if len(body) > MaxStoredBody {
		out := make([]byte, MaxStoredBody)
		copy(out, body)
		return out, true
	}
	return bytes.Clone(body), false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
