package ingress

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/ingress/sig"
)

func TestGitHubPublishedVectorAndRedaction(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	metrics := &Metrics{}
	now := time.Unix(1_700_000_000, 0).UTC()
	v, err := sig.Open("github", "It's a Secret to Everybody")
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Provider: ProviderGitHub, Verifier: v, Sink: store, Now: func() time.Time { return now }, Metrics: metrics, WebhookUID: ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingress/github", strings.NewReader("Hello, World!"))
	req.Header.Set("X-Hub-Signature-256", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Gitlab-Token", "nope")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.Bytes())
	}
	var ack ackBody
	if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil || ack.Duplicate {
		t.Fatalf("ack %s err %v", rr.Body.Bytes(), err)
	}
	if metrics.VerifyFailed() != 0 || store.count() != 1 {
		t.Fatalf("metrics %d rows %d", metrics.VerifyFailed(), store.count())
	}
	got := store.one()
	if got.Provider != ProviderGitHub || got.DeliveryID != "delivery-1" || string(got.Body) != "Hello, World!" || got.Truncated {
		t.Fatalf("delivery %+v body %q", got, got.Body)
	}
	if !got.ReceivedAt.Equal(now) || !strings.HasPrefix(got.ID, PrefixDelivery) {
		t.Fatalf("id %s at %s", got.ID, got.ReceivedAt)
	}
	if got.Headers["X-Github-Event"] != "issues" || got.Headers["X-Github-Delivery"] != "delivery-1" {
		t.Fatalf("headers %+v", got.Headers)
	}
	for _, forbidden := range []string{"Authorization", "Cookie", "X-Hub-Signature-256", "X-Gitlab-Token"} {
		if _, ok := got.Headers[forbidden]; ok {
			t.Fatalf("stored %s", forbidden)
		}
		if _, ok := got.Headers[http.CanonicalHeaderKey(forbidden)]; ok {
			t.Fatalf("stored canonical %s", forbidden)
		}
	}
}

func TestDuplicateDelivery(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := githubHandler(store, okVerifier{})
	for i, wantDup := range []bool{false, true} {
		rr := postGitHub(h, "same", []byte("body"))
		if rr.Code != http.StatusOK {
			t.Fatalf("try %d status %d", i, rr.Code)
		}
		var ack ackBody
		if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil {
			t.Fatal(err)
		}
		if ack.Duplicate != wantDup {
			t.Fatalf("try %d duplicate %v", i, ack.Duplicate)
		}
	}
	if store.count() != 1 {
		t.Fatalf("rows %d", store.count())
	}
}

func TestVerifyFailureDoesNotUnmarshal(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	metrics := &Metrics{}
	spy := &spyNormalizer{}
	h := New(Config{
		Provider: ProviderSlack,
		Verifier: failVerifier{},
		Sink:     store,
		Metrics:  metrics,
		parser:   spy,
	})
	body := `{"type":"url_verification","challenge":"secret-challenge","event_id":"Ev1"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rr.Code)
	}
	if spy.parseCalls != 0 || spy.normCalls != 0 {
		t.Fatalf("parse %d normalize %d", spy.parseCalls, spy.normCalls)
	}
	if store.calls() != 0 || metrics.VerifyFailed() != 1 {
		t.Fatalf("enqueues %d verifyFailed %d", store.calls(), metrics.VerifyFailed())
	}
	if strings.Contains(rr.Body.String(), "secret-challenge") {
		t.Fatalf("challenge leaked: %s", rr.Body.String())
	}
}

func TestVerifyFailureBeforeMissingDeliveryID(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := githubHandler(store, failVerifier{})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Hello, World!"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || store.calls() != 0 {
		t.Fatalf("status %d enqueues %d", rr.Code, store.calls())
	}
}

func TestSlackChallengeAndEventID(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	spy := &spyNormalizer{}
	now := time.Unix(1_700_000_100, 0)
	h := New(Config{
		Provider: ProviderSlack,
		Verifier: okVerifier{},
		Sink:     store,
		Now:      func() time.Time { return now },
		parser:   spy,
	})
	challenge := `{"type":"url_verification","challenge":"abc<&>"}`
	rr := post(h, strings.NewReader(challenge), nil)
	if rr.Code != http.StatusOK || store.calls() != 0 || spy.parseCalls != 1 || spy.normCalls != 0 {
		t.Fatalf("challenge status %d enqueues %d parse %d norm %d body %s", rr.Code, store.calls(), spy.parseCalls, spy.normCalls, rr.Body.Bytes())
	}
	var got challengeBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.Challenge != "abc<&>" {
		t.Fatalf("body %s err %v", rr.Body.Bytes(), err)
	}

	eventBody := `{"type":"event_callback","event_id":"Ev42","event":{"type":"message"}}`
	rr = post(h, strings.NewReader(eventBody), nil)
	if rr.Code != http.StatusOK || store.count() != 1 || store.one().DeliveryID != "Ev42" {
		t.Fatalf("event status %d id %s", rr.Code, store.one().DeliveryID)
	}

	rr = post(h, strings.NewReader(eventBody), map[string]string{"X-Slack-Retry-Num": "1"})
	var ack ackBody
	if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil || !ack.Duplicate || store.count() != 1 {
		t.Fatalf("retry ack %s rows %d", rr.Body.Bytes(), store.count())
	}

	other := `{"type":"event_callback","event_id":"Ev43"}`
	rr = post(h, strings.NewReader(other), map[string]string{"X-Slack-Retry-Num": "2"})
	if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil || ack.Duplicate || store.count() != 2 {
		t.Fatalf("other retry ack %s rows %d", rr.Body.Bytes(), store.count())
	}
	if store.one().Headers["X-Slack-Retry-Num"] == "" && !headerHasRetry(store) {
		t.Fatal("retry header was not retained")
	}
}

func TestSlackSignedEvent(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	const secret = "slack-secret"
	const ts = "1700000100"
	now := time.Unix(1700000100, 0)
	v, err := sig.Open("slack", secret)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Provider: ProviderSlack, Verifier: v, Sink: store, Now: func() time.Time { return now }})
	body := []byte(`{"type":"event_callback","event_id":"Ev9"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSig(secret, ts, body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || store.one().DeliveryID != "Ev9" {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.Bytes())
	}
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0=00")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig status %d", rr.Code)
	}
}

func TestBodyLimits(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	seen := &captureVerifier{}
	metrics := &Metrics{}
	h := New(Config{Provider: ProviderGitHub, Verifier: seen, Sink: store, Metrics: metrics})

	over := httptest.NewRequest(http.MethodPost, "/", nil)
	over.ContentLength = MaxBody + 1
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, over)
	if rr.Code != http.StatusRequestEntityTooLarge || seen.calls != 0 || store.calls() != 0 || metrics.TooLarge() != 1 || metrics.VerifyFailed() != 0 {
		t.Fatalf("content-length status %d calls %d enqueues %d", rr.Code, seen.calls, store.calls())
	}

	big := bytes.Repeat([]byte("a"), MaxBody+1)
	rr = postGitHub(h, "big", big)
	if rr.Code != http.StatusRequestEntityTooLarge || seen.calls != 0 {
		t.Fatalf("reader status %d calls %d", rr.Code, seen.calls)
	}

	full := bytes.Repeat([]byte("b"), MaxStoredBody+8)
	rr = postGitHub(h, "trunc", full)
	if rr.Code != http.StatusOK {
		t.Fatalf("trunc status %d", rr.Code)
	}
	got := store.one()
	if !got.Truncated || len(got.Body) != MaxStoredBody || seen.bodyLen != len(full) {
		t.Fatalf("stored %d truncated %v verified %d", len(got.Body), got.Truncated, seen.bodyLen)
	}
	if !bytes.Equal(got.Body, full[:MaxStoredBody]) {
		t.Fatal("stored prefix mismatch")
	}
}

func TestMissingDeliveryIDAndMethod(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := githubHandler(store, okVerifier{})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || store.calls() != 0 {
		t.Fatalf("missing id status %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method %d allow %s", rr.Code, rr.Header().Get("Allow"))
	}
}

func TestSinkErrorIsGeneric(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	store.fail = errors.New("db password=secret")
	h := githubHandler(store, okVerifier{})
	rr := postGitHub(h, "d", []byte("x"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "password") || store.count() != 0 {
		t.Fatalf("body %s rows %d", rr.Body.String(), store.count())
	}
}

func TestLinearAndWebhookIDs(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := New(Config{Provider: ProviderLinear, Verifier: okVerifier{}, Sink: store})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Linear-Delivery", "lin-1")
	req.Header.Set("Linear-Signature", "deadbeef")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || store.one().DeliveryID != "lin-1" {
		t.Fatalf("linear %d %+v", rr.Code, store.one())
	}
	if _, ok := store.one().Headers["Linear-Signature"]; ok {
		t.Fatal("stored linear signature")
	}

	wh := New(Config{
		Provider:         ProviderWebhook,
		Verifier:         okVerifier{},
		Sink:             store,
		WebhookUID:       "whk_1",
		DeliveryIDHeader: "X-CI-Run-Id",
	})
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}`))
	req.Header.Set("X-CI-Run-Id", "run-9")
	rr = httptest.NewRecorder()
	wh.ServeHTTP(rr, req)
	got := findDelivery(store, "run-9")
	if rr.Code != http.StatusOK || got.WebhookUID != "whk_1" || got.DeliveryID != "run-9" {
		t.Fatalf("webhook status %d %+v", rr.Code, got)
	}
}

func TestHandlerConcurrentEnqueue(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := githubHandler(store, okVerifier{})
	const n = 32
	errc := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rr := postGitHub(h, fmt.Sprintf("d-%d", i), []byte(`{"n":1}`))
			if rr.Code != http.StatusOK {
				errc <- fmt.Errorf("status %d", rr.Code)
			}
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if store.count() != n {
		t.Fatalf("rows %d", store.count())
	}
}

func TestHandlerP99Under50ms(t *testing.T) {
	store := newMemStore()
	h := githubHandler(store, okVerifier{})
	const n = 200
	samples := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
		req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("p99-%d", i))
		req.Header.Set("X-GitHub-Event", "ping")
		rr := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rr, req)
		samples[i] = time.Since(start)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d", rr.Code)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(math.Ceil(0.99*float64(n))) - 1
	if samples[idx] >= 50*time.Millisecond {
		t.Fatalf("p99 %s max %s", samples[idx], samples[n-1])
	}
}

type okVerifier struct{}

func (okVerifier) Verify(http.Header, []byte, time.Time) error { return nil }

type failVerifier struct{}

func (failVerifier) Verify(http.Header, []byte, time.Time) error {
	return errors.New("nope")
}

type captureVerifier struct {
	calls   int
	bodyLen int
}

func (c *captureVerifier) Verify(_ http.Header, body []byte, _ time.Time) error {
	c.calls++
	c.bodyLen = len(body)
	return nil
}

type spyNormalizer struct {
	parseCalls int
	normCalls  int
}

func (s *spyNormalizer) Parse(body []byte) (slackEnvelope, error) {
	s.parseCalls++
	return stdSlackParser{}.Parse(body)
}

func (s *spyNormalizer) Normalize(Delivery) ([]event.Event, error) {
	s.normCalls++
	return nil, nil
}

var _ Normalizer = (*spyNormalizer)(nil)

func githubHandler(store *memStore, v Verifier) http.Handler {
	return New(Config{Provider: ProviderGitHub, Verifier: v, Sink: store})
}

func postGitHub(h http.Handler, id string, body []byte) *httptest.ResponseRecorder {
	return post(h, bytes.NewReader(body), map[string]string{
		"X-GitHub-Delivery": id,
		"X-GitHub-Event":    "issues",
	})
}

func post(h http.Handler, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func slackSig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + ts + ":"))
	_, _ = mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func headerHasRetry(store *memStore) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, d := range store.all {
		if d.Headers["X-Slack-Retry-Num"] != "" {
			return true
		}
	}
	return false
}

func findDelivery(store *memStore, deliveryID string) Delivery {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, d := range store.all {
		if d.DeliveryID == deliveryID {
			return cloneDelivery(d)
		}
	}
	return Delivery{}
}
