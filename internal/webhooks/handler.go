package webhooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/ingress/sig"
	"github.com/polds/factory/internal/store"
)

// Lookup loads one registration. *store.Store implements it.
// GetWebhookRegistrationByTokenHash takes a SHA-256 digest, never the token.
type Lookup interface {
	GetWebhookRegistration(ctx context.Context, uid string) (store.WebhookRegistration, error)
	GetWebhookRegistrationByTokenHash(ctx context.Context, sum []byte) (store.WebhookRegistration, error)
}

// Sink persists a verified delivery. Enqueue is idempotent on
// (provider, webhook uid, delivery id): a duplicate returns dup true and a
// nil error. factoryID is the registration's factory. The body is capped
// and the headers are redacted before the call.
type Sink interface {
	Enqueue(ctx context.Context, factoryID string, d ingress.Delivery) (dup bool, err error)
}

// Config builds the custom-webhook handler.
// Now, when set, is the clock for signature skew and the body-hash bucket.
// Log, when set, receives access lines with the url_token redacted.
type Config struct {
	Lookup  Lookup
	Secrets SecretSource
	Sink    Sink
	Now     func() time.Time
	Log     *slog.Logger
	Metrics *Metrics
}

// Metrics counts ingress outcomes that are not persisted.
// Do not copy a Metrics value.
type Metrics struct {
	verifyFailed atomic.Int64
	tooLarge     atomic.Int64
	disabled     atomic.Int64
}

// VerifyFailed is the number of requests rejected before parse.
func (m *Metrics) VerifyFailed() int64 {
	if m == nil {
		return 0
	}
	return m.verifyFailed.Load()
}

// TooLarge is the number of requests rejected for exceeding 1 MiB.
func (m *Metrics) TooLarge() int64 {
	if m == nil {
		return 0
	}
	return m.tooLarge.Load()
}

// Disabled is the number of requests rejected because the webhook is off.
func (m *Metrics) Disabled() int64 {
	if m == nil {
		return 0
	}
	return m.disabled.Load()
}

func (m *Metrics) addVerifyFailed() {
	if m != nil {
		m.verifyFailed.Add(1)
	}
}

func (m *Metrics) addTooLarge() {
	if m != nil {
		m.tooLarge.Add(1)
	}
}

func (m *Metrics) addDisabled() {
	if m != nil {
		m.disabled.Add(1)
	}
}

type routeKind int

const (
	routeUID routeKind = iota
	routeURL
)

func neverRoute(r routeKind) routeKind { return r }

// Handler is the custom-webhook ingress, mounted at /.
// POST /api/v1/webhooks/{webhook_uid} serves token and signature.
// POST /api/v1/webhooks/u/{url_token} serves url_token.
func Handler(cfg Config) http.Handler {
	h := &handler{
		lookup:  cfg.Lookup,
		secrets: cfg.Secrets,
		sink:    cfg.Sink,
		now:     cfg.Now,
		metrics: cfg.Metrics,
	}
	if h.now == nil {
		h.now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+URLTokenPrefix+"{token}", h.serveURLToken)
	mux.HandleFunc("POST "+PathPrefix+"{uid}", h.serveUID)
	var out http.Handler = mux
	if cfg.Log != nil {
		out = AccessLog(cfg.Log, out)
	}
	return out
}

type handler struct {
	lookup  Lookup
	secrets SecretSource
	sink    Sink
	now     func() time.Time
	metrics *Metrics
}

func (h *handler) serveUID(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, routeUID, r.PathValue("uid"))
}

func (h *handler) serveURLToken(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, routeURL, r.PathValue("token"))
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, route routeKind, key string) {
	if h.lookup == nil || h.sink == nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	reg, ok := h.lookupReg(w, r, route, key)
	if !ok {
		return
	}
	if !reg.Enabled {
		h.metrics.addDisabled()
		writeJSON(w, http.StatusGone, errorBody{Error: "gone"})
		return
	}
	if !routeAllows(route, reg.AuthMode) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
		return
	}
	if reg.FactoryID == "" || reg.UID == "" {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	body, err := ingress.ReadBody(r)
	if errors.Is(err, ingress.ErrTooLarge) {
		h.metrics.addTooLarge()
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{Error: "payload too large"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
		return
	}
	now := h.now()
	if err := h.verify(r, reg, body, now); err != nil {
		h.metrics.addVerifyFailed()
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthorized"})
		return
	}
	deliveryID, err := DeliveryID(reg.DeliveryIDHeader, r.Header, body, now)
	if errors.Is(err, errMissingDeliveryID) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "bad request"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	id, err := newDeliveryID(now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	stored, truncated := ingress.ClipBody(body)
	dup, err := h.sink.Enqueue(r.Context(), reg.FactoryID, ingress.Delivery{
		ID:         id,
		Provider:   ingress.ProviderWebhook,
		WebhookUID: reg.UID,
		DeliveryID: deliveryID,
		ReceivedAt: now.UTC(),
		Headers:    ingress.RedactHeaders(r.Header),
		Body:       stored,
		Truncated:  truncated,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, ackBody{Duplicate: dup})
}

func (h *handler) lookupReg(w http.ResponseWriter, r *http.Request, route routeKind, key string) (store.WebhookRegistration, bool) {
	var (
		reg store.WebhookRegistration
		err error
	)
	switch route {
	case routeUID:
		if !validUID(key) {
			writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
			return store.WebhookRegistration{}, false
		}
		reg, err = h.lookup.GetWebhookRegistration(r.Context(), key)
	case routeURL:
		if !ValidURLToken(key) {
			writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
			return store.WebhookRegistration{}, false
		}
		sum := sha256.Sum256([]byte(key))
		reg, err = h.lookup.GetWebhookRegistrationByTokenHash(r.Context(), sum[:])
	default:
		_ = neverRoute(route)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return store.WebhookRegistration{}, false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
		return store.WebhookRegistration{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return store.WebhookRegistration{}, false
	}
	return reg, true
}

func routeAllows(route routeKind, mode string) bool {
	switch route {
	case routeUID:
		switch authMode(mode) {
		case authToken, authSignature:
			return true
		case authURLToken:
			return false
		default:
			return false
		}
	case routeURL:
		return authMode(mode) == authURLToken
	default:
		_ = neverRoute(route)
		return false
	}
}

func (h *handler) verify(r *http.Request, reg store.WebhookRegistration, body []byte, now time.Time) error {
	switch authMode(reg.AuthMode) {
	case authToken:
		secret, err := h.secret(r, reg.SecretRef)
		if err != nil || !bearerMatch(r.Header.Get("Authorization"), secret) {
			return errUnauthorized
		}
		return nil
	case authURLToken:
		return nil
	case authSignature:
		secret, err := h.secret(r, reg.SecretRef)
		if err != nil {
			return errUnauthorized
		}
		v, err := sig.Open(reg.SignatureScheme, secret)
		if err != nil {
			return errUnauthorized
		}
		if err := v.Verify(r.Header, body, now); err != nil {
			return errUnauthorized
		}
		return nil
	default:
		return fmt.Errorf("webhooks: unsupported authMode %q", string(neverAuth(authMode(reg.AuthMode))))
	}
}

func (h *handler) secret(r *http.Request, name string) (string, error) {
	if h.secrets == nil || name == "" {
		return "", errUnauthorized
	}
	value, err := h.secrets.Secret(r.Context(), name)
	if err != nil || value == "" {
		return "", errUnauthorized
	}
	return value, nil
}

var errUnauthorized = errors.New("webhooks: unauthorized")

func newDeliveryID(now time.Time) (string, error) {
	var entropy [10]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("webhooks: id entropy: %w", err)
	}
	ms := now.UnixMilli()
	if ms < 0 {
		return "", fmt.Errorf("webhooks: delivery time is before unix epoch")
	}
	id, err := store.FormatID(store.PrefixDelivery, uint64(ms), entropy[:])
	if err != nil {
		return "", fmt.Errorf("webhooks: delivery id: %w", err)
	}
	return id, nil
}

type ackBody struct {
	Duplicate bool `json:"duplicate"`
}

type errorBody struct {
	Error string `json:"error"`
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
