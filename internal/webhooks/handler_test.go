package webhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/store"
)

const (
	testSecret = "bearer-secret-z9q"
	testUID    = "whk_ci"
	testFac    = "fac_1"
)

func TestTokenModeAcceptsBearerAndRedactsIt(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	var log bytes.Buffer
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, "X-CI-Run-Id"),
	}}, sink, &log)
	rr := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
		"Authorization": "Bearer " + testSecret,
		"Cookie":        "session=secret",
		"X-CI-Run-Id":   "run-9",
		"Content-Type":  "application/json",
	}, []byte(`{"action":"opened"}`))
	if rr.Code != http.StatusOK || ackDuplicate(t, rr) {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.Bytes())
	}
	got := sink.one(t)
	if got.factoryID != testFac || got.d.WebhookUID != testUID || got.d.Provider != ingress.ProviderWebhook || got.d.DeliveryID != "run-9" {
		t.Fatalf("%+v", got)
	}
	if string(got.d.Body) != `{"action":"opened"}` || got.d.Truncated || !strings.HasPrefix(got.d.ID, store.PrefixDelivery) {
		t.Fatalf("%+v body %s", got.d, got.d.Body)
	}
	for _, forbidden := range []string{"Authorization", "Cookie"} {
		if _, ok := got.d.Headers[forbidden]; ok {
			t.Fatalf("stored %s in %+v", forbidden, got.d.Headers)
		}
	}
	if strings.Contains(log.String(), testSecret) || strings.Contains(rr.Body.String(), testSecret) {
		t.Fatalf("secret leaked log=%s body=%s", log.String(), rr.Body.String())
	}
}

func TestTokenModeRejectsBadBearerWithoutPersisting(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	metrics := &Metrics{}
	h := handlerWith(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, "X-CI-Run-Id"),
	}}, sink, metrics, nil)
	for _, auth := range []string{"", "Bearer wrong-secret-z9q", "bearer " + testSecret, testSecret} {
		rr := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
			"Authorization": auth,
			"X-CI-Run-Id":   "run-9",
		}, []byte(`not json`))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q status %d", auth, rr.Code)
		}
	}
	if sink.count() != 0 || metrics.VerifyFailed() != 4 {
		t.Fatalf("rows %d verify %d", sink.count(), metrics.VerifyFailed())
	}
}

func TestSignatureModes(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_492_774_577, 0).UTC()
	cases := []struct {
		name    string
		scheme  string
		secret  string
		body    string
		headers map[string]string
		when    time.Time
		want    int
	}{
		{
			name: "github", scheme: "github", secret: "It's a Secret to Everybody",
			body: "Hello, World!",
			headers: map[string]string{
				"X-Hub-Signature-256": "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17",
				"X-CI-Run-Id":         "gh-1",
			},
			when: time.Unix(0, 0).UTC(), want: http.StatusOK,
		},
		{
			name: "github tampered", scheme: "github", secret: "It's a Secret to Everybody",
			body: "Hello, World!?",
			headers: map[string]string{
				"X-Hub-Signature-256": "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17",
				"X-CI-Run-Id":         "gh-2",
			},
			when: time.Unix(0, 0).UTC(), want: http.StatusUnauthorized,
		},
		{
			name: "gitlab", scheme: "gitlab", secret: "gitlab-secret-token",
			body: `{"object_kind":"push"}`,
			headers: map[string]string{
				"X-Gitlab-Token": "gitlab-secret-token",
				"X-CI-Run-Id":    "gl-1",
			},
			when: time.Unix(0, 0).UTC(), want: http.StatusOK,
		},
		{
			name: "stripe", scheme: "stripe", secret: "whsec_test_secret",
			body: `{"id":"evt_test_webhook","object":"event"}`,
			headers: map[string]string{
				"Stripe-Signature": "t=1492774577,v1=88a022085c6bdb887b02cb26ff76dd681234d9675c0f22844059f55552a8883a",
				"X-CI-Run-Id":      "st-1",
			},
			when: now, want: http.StatusOK,
		},
		{
			name: "stripe stale", scheme: "stripe", secret: "whsec_test_secret",
			body: `{"id":"evt_test_webhook","object":"event"}`,
			headers: map[string]string{
				"Stripe-Signature": "t=1492774577,v1=88a022085c6bdb887b02cb26ff76dd681234d9675c0f22844059f55552a8883a",
				"X-CI-Run-Id":      "st-2",
			},
			when: now.Add(6 * time.Minute), want: http.StatusUnauthorized,
		},
		{
			name: "standard webhooks", scheme: "standard_webhooks", secret: "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
			body: `{"test": 2432232314}`,
			headers: map[string]string{
				"Webhook-Id":        "msg_p5jXN8AQM9LWM0D4loKWxJek",
				"Webhook-Timestamp": "1614265330",
				"Webhook-Signature": "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=",
				"X-CI-Run-Id":       "std-1",
			},
			when: time.Unix(1_614_265_330, 0).UTC(), want: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &memSink{}
			secrets := &memSecrets{values: map[string]string{"CI_WEBHOOK_SECRET": tc.secret}}
			reg := tokenReg(true, "X-CI-Run-Id")
			reg.AuthMode = store.WebhookAuthSignature
			reg.SignatureScheme = tc.scheme
			h := Handler(Config{
				Lookup:  &memLookup{byUID: map[string]store.WebhookRegistration{testUID: reg}},
				Secrets: secrets,
				Sink:    sink,
				Now:     func() time.Time { return tc.when },
			})
			rr := post(h, "/api/v1/webhooks/"+testUID, tc.headers, []byte(tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status %d body %s", rr.Code, rr.Body.Bytes())
			}
			if tc.want != http.StatusOK {
				if sink.count() != 0 {
					t.Fatal("persisted a failed verify")
				}
				return
			}
			got := sink.one(t)
			if _, ok := got.d.Headers["X-Hub-Signature-256"]; ok {
				t.Fatalf("stored signature %+v", got.d.Headers)
			}
			if _, ok := got.d.Headers["Webhook-Signature"]; ok {
				t.Fatalf("stored standard signature %+v", got.d.Headers)
			}
			if strings.Contains(rr.Body.String(), tc.secret) {
				t.Fatal("secret in response")
			}
		})
	}
}

func TestNonJSONAcksWhenVerified(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, ""),
	}}, sink, nil)
	body := []byte("Hello, World!")
	rr := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
		"Authorization": "Bearer " + testSecret,
	}, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	got := sink.one(t)
	if _, err := Normalize(got.d); err == nil {
		t.Fatal("normalize accepted non-JSON")
	}
	again := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
		"Authorization": "Bearer " + testSecret,
	}, body)
	if again.Code != http.StatusOK || !ackDuplicate(t, again) {
		t.Fatalf("second status %d body %s", again.Code, again.Body.Bytes())
	}
	if sink.count() != 1 {
		t.Fatalf("rows %d", sink.count())
	}
}

func TestBodyHashBucketSeparatesWindows(t *testing.T) {
	t.Parallel()
	aligned := time.Unix((1_700_000_000/300)*300, 0).UTC()
	sink := &memSink{}
	lookup := &memLookup{byUID: map[string]store.WebhookRegistration{testUID: tokenReg(true, "")}}
	body := []byte(`{"action":"opened"}`)
	var when time.Time
	h := Handler(Config{
		Lookup: lookup, Secrets: testSecrets(), Sink: sink,
		Now: func() time.Time { return when },
	})
	when = aligned
	first := post(h, "/api/v1/webhooks/"+testUID, bearer(), body)
	when = aligned.Add(time.Second)
	second := post(h, "/api/v1/webhooks/"+testUID, bearer(), body)
	when = aligned.Add(DedupBucket)
	third := post(h, "/api/v1/webhooks/"+testUID, bearer(), body)
	if first.Code != http.StatusOK || ackDuplicate(t, first) {
		t.Fatalf("first %d %s", first.Code, first.Body.Bytes())
	}
	if second.Code != http.StatusOK || !ackDuplicate(t, second) {
		t.Fatalf("second %d %s", second.Code, second.Body.Bytes())
	}
	if third.Code != http.StatusOK || ackDuplicate(t, third) {
		t.Fatalf("third %d %s", third.Code, third.Body.Bytes())
	}
	if sink.count() != 2 {
		t.Fatalf("rows %d", sink.count())
	}
}

func TestURLTokenMode(t *testing.T) {
	t.Parallel()
	const token = "url-token-z9q.ABC_~"
	sum := sha256.Sum256([]byte(token))
	reg := tokenReg(true, "X-CI-Run-Id")
	reg.AuthMode = store.WebhookAuthURLToken
	reg.URLTokenHash = sum[:]
	sink := &memSink{}
	var log bytes.Buffer
	secrets := &memSecrets{err: errCalled}
	h := Handler(Config{
		Lookup:  &memLookup{byHash: map[string]store.WebhookRegistration{hex.EncodeToString(sum[:]): reg}},
		Secrets: secrets,
		Sink:    sink,
		Log:     slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:     func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
	})
	rr := post(h, "/api/v1/webhooks/u/"+token, map[string]string{"X-CI-Run-Id": "run-9"}, []byte(`{"ok":true}`))
	if rr.Code != http.StatusOK || ackDuplicate(t, rr) {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.Bytes())
	}
	got := sink.one(t)
	if got.d.WebhookUID != testUID || got.d.DeliveryID != "run-9" {
		t.Fatalf("%+v", got.d)
	}
	if strings.Contains(log.String(), token) || !strings.Contains(log.String(), "{redacted}") {
		t.Fatalf("log %s", log.String())
	}
	if len(secrets.calls) != 0 {
		t.Fatalf("resolved a secret for url_token: %v", secrets.calls)
	}
	missing := post(h, "/api/v1/webhooks/u/other-token-z9q", nil, []byte(`{"ok":true}`))
	if missing.Code != http.StatusNotFound || sink.count() != 1 {
		t.Fatalf("missing status %d rows %d", missing.Code, sink.count())
	}
	bad := post(h, "/api/v1/webhooks/u/has%20space-token", nil, []byte(`{}`))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("charset status %d", bad.Code)
	}
	if strings.Contains(log.String(), "has space") || strings.Contains(log.String(), "has%20space") {
		t.Fatalf("bad token logged %s", log.String())
	}
}

func TestDisabledIsGoneAndPersistsNothing(t *testing.T) {
	t.Parallel()
	const token = "url-token-z9q.ABC_~"
	sum := sha256.Sum256([]byte(token))
	urlReg := tokenReg(false, "")
	urlReg.AuthMode = store.WebhookAuthURLToken
	urlReg.URLTokenHash = append([]byte(nil), sum[:]...)
	sigReg := tokenReg(false, "X-CI-Run-Id")
	sigReg.AuthMode = store.WebhookAuthSignature
	sigReg.SignatureScheme = "github"
	sink := &memSink{}
	secrets := &memSecrets{err: errCalled}
	metrics := &Metrics{}
	body := &sniff{body: strings.NewReader(`{"ok":true}`)}
	h := Handler(Config{
		Lookup: &memLookup{
			byUID: map[string]store.WebhookRegistration{testUID: sigReg, "whk_url": urlReg},
			byHash: map[string]store.WebhookRegistration{
				hex.EncodeToString(sum[:]): urlReg,
			},
		},
		Secrets: secrets,
		Sink:    sink,
		Metrics: metrics,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+testUID, body)
	req.Header.Set("X-Hub-Signature-256", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
	req.ContentLength = ingress.MaxBody + 1
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusGone || body.read {
		t.Fatalf("status %d read %v", rr.Code, body.read)
	}
	urlReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/u/"+token, strings.NewReader("x"))
	urlRR := httptest.NewRecorder()
	h.ServeHTTP(urlRR, urlReq)
	uidOfURL := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/whk_url", strings.NewReader("x"))
	uidRR := httptest.NewRecorder()
	h.ServeHTTP(uidRR, uidOfURL)
	if urlRR.Code != http.StatusGone || uidRR.Code != http.StatusGone {
		t.Fatalf("url %d uid %d", urlRR.Code, uidRR.Code)
	}
	if sink.count() != 0 || metrics.Disabled() != 3 || len(secrets.calls) != 0 {
		t.Fatalf("rows %d disabled %d calls %v", sink.count(), metrics.Disabled(), secrets.calls)
	}
}

func TestEnabledURLTokenOnUIDRouteIsNotFound(t *testing.T) {
	t.Parallel()
	reg := tokenReg(true, "")
	reg.AuthMode = store.WebhookAuthURLToken
	sink := &memSink{}
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{testUID: reg}}, sink, nil)
	rr := post(h, "/api/v1/webhooks/"+testUID, bearer(), []byte(`{}`))
	if rr.Code != http.StatusNotFound || sink.count() != 0 {
		t.Fatalf("status %d rows %d", rr.Code, sink.count())
	}
}

func TestMissingDeliveryHeaderDoesNotPersist(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, "X-CI-Run-Id"),
	}}, sink, nil)
	rr := post(h, "/api/v1/webhooks/"+testUID, bearer(), []byte(`{}`))
	if rr.Code != http.StatusBadRequest || sink.count() != 0 {
		t.Fatalf("status %d rows %d", rr.Code, sink.count())
	}
}

func TestRejectedDeliveryHeaderIsNotADedupKey(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Authorization", "Cookie", "X-Hub-Signature-256"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &memSink{}
			reg := tokenReg(true, name)
			h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{testUID: reg}}, sink, nil)
			bad := post(h, "/api/v1/webhooks/"+testUID, map[string]string{"Authorization": "Bearer nope"}, []byte(`{}`))
			if bad.Code != http.StatusUnauthorized || sink.count() != 0 {
				t.Fatalf("bad auth status %d rows %d", bad.Code, sink.count())
			}
			headers := map[string]string{"Authorization": "Bearer " + testSecret}
			if name != "Authorization" {
				headers[name] = "secret-value"
			}
			rr := post(h, "/api/v1/webhooks/"+testUID, headers, []byte(`{}`))
			if rr.Code != http.StatusInternalServerError || sink.count() != 0 {
				t.Fatalf("status %d rows %d body %s", rr.Code, sink.count(), rr.Body.Bytes())
			}
		})
	}
}

func TestUnknownWebhook(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := testHandler(t, &memLookup{}, sink, nil)
	rr := post(h, "/api/v1/webhooks/whk_missing", bearer(), []byte(`{}`))
	if rr.Code != http.StatusNotFound || sink.count() != 0 {
		t.Fatalf("status %d rows %d", rr.Code, sink.count())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/"+testUID, nil)
	getRR := httptest.NewRecorder()
	h.ServeHTTP(getRR, get)
	if getRR.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get %d", getRR.Code)
	}
}

func TestTooLargeDoesNotPersist(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	metrics := &Metrics{}
	h := handlerWith(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, ""),
	}}, sink, metrics, nil)
	body := bytes.Repeat([]byte("a"), ingress.MaxBody+1)
	rr := post(h, "/api/v1/webhooks/"+testUID, bearer(), body)
	if rr.Code != http.StatusRequestEntityTooLarge || sink.count() != 0 || metrics.TooLarge() != 1 {
		t.Fatalf("status %d rows %d tooLarge %d", rr.Code, sink.count(), metrics.TooLarge())
	}
}

func TestStoredBodyIsCapped(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, "X-CI-Run-Id"),
	}}, sink, nil)
	body := bytes.Repeat([]byte("b"), ingress.MaxStoredBody+10)
	rr := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
		"Authorization": "Bearer " + testSecret,
		"X-CI-Run-Id":   "big",
	}, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	got := sink.one(t)
	if !got.d.Truncated || len(got.d.Body) != ingress.MaxStoredBody {
		t.Fatalf("truncated %v len %d", got.d.Truncated, len(got.d.Body))
	}
}

func TestConcurrentDuplicate(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := testHandler(t, &memLookup{byUID: map[string]store.WebhookRegistration{
		testUID: tokenReg(true, "X-CI-Run-Id"),
	}}, sink, nil)
	const n = 16
	codes := make([]int, n)
	dups := make([]bool, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			rr := post(h, "/api/v1/webhooks/"+testUID, map[string]string{
				"Authorization": "Bearer " + testSecret,
				"X-CI-Run-Id":   "same",
			}, []byte(`{"n":1}`))
			codes[i] = rr.Code
			var ack ackBody
			_ = json.Unmarshal(rr.Body.Bytes(), &ack)
			dups[i] = ack.Duplicate
		}()
	}
	wg.Wait()
	fresh := 0
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusOK {
			t.Fatalf("status %d", codes[i])
		}
		if !dups[i] {
			fresh++
		}
	}
	if fresh != 1 || sink.count() != 1 {
		t.Fatalf("fresh %d rows %d", fresh, sink.count())
	}
}

func TestNilSecretsFailVerify(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	h := Handler(Config{
		Lookup: &memLookup{byUID: map[string]store.WebhookRegistration{testUID: tokenReg(true, "")}},
		Sink:   sink,
		Now:    func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
	})
	rr := post(h, "/api/v1/webhooks/"+testUID, bearer(), []byte(`{}`))
	if rr.Code != http.StatusUnauthorized || sink.count() != 0 {
		t.Fatalf("status %d rows %d", rr.Code, sink.count())
	}
}

var errCalled = errString("secret source called")

type errString string

func (e errString) Error() string { return string(e) }

func tokenReg(enabled bool, header string) store.WebhookRegistration {
	return store.WebhookRegistration{
		UID:              testUID,
		FactoryID:        testFac,
		Name:             "ci",
		AuthMode:         store.WebhookAuthToken,
		SecretRef:        "CI_WEBHOOK_SECRET",
		DeliveryIDHeader: header,
		Enabled:          enabled,
	}
}

func testSecrets() *memSecrets {
	return &memSecrets{values: map[string]string{"CI_WEBHOOK_SECRET": testSecret}}
}

func bearer() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testSecret}
}

func testHandler(t *testing.T, lookup Lookup, sink Sink, log *bytes.Buffer) http.Handler {
	t.Helper()
	var logger *slog.Logger
	if log != nil {
		logger = slog.New(slog.NewJSONHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return handlerWith(t, lookup, sink, nil, logger)
}

func handlerWith(t *testing.T, lookup Lookup, sink Sink, metrics *Metrics, log *slog.Logger) http.Handler {
	t.Helper()
	return Handler(Config{
		Lookup:  lookup,
		Secrets: testSecrets(),
		Sink:    sink,
		Now:     func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
		Log:     log,
		Metrics: metrics,
	})
}

func post(h http.Handler, path string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func ackDuplicate(t *testing.T, rr *httptest.ResponseRecorder) bool {
	t.Helper()
	var ack ackBody
	if err := json.Unmarshal(rr.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	return ack.Duplicate
}

type stored struct {
	factoryID string
	d         ingress.Delivery
}

type memSink struct {
	mu   sync.Mutex
	rows []stored
}

func (m *memSink) Enqueue(_ context.Context, factoryID string, d ingress.Delivery) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.d.Provider == d.Provider && row.d.WebhookUID == d.WebhookUID && row.d.DeliveryID == d.DeliveryID {
			return true, nil
		}
	}
	m.rows = append(m.rows, stored{factoryID: factoryID, d: d})
	return false, nil
}

func (m *memSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *memSink) one(t *testing.T) stored {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.rows) != 1 {
		t.Fatalf("rows %d", len(m.rows))
	}
	return m.rows[0]
}

type memLookup struct {
	mu     sync.Mutex
	byUID  map[string]store.WebhookRegistration
	byHash map[string]store.WebhookRegistration
}

func (m *memLookup) GetWebhookRegistration(_ context.Context, uid string) (store.WebhookRegistration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, ok := m.byUID[uid]
	if !ok {
		return store.WebhookRegistration{}, store.ErrNotFound
	}
	return reg, nil
}

func (m *memLookup) GetWebhookRegistrationByTokenHash(_ context.Context, sum []byte) (store.WebhookRegistration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, ok := m.byHash[hex.EncodeToString(sum)]
	if !ok {
		return store.WebhookRegistration{}, store.ErrNotFound
	}
	return reg, nil
}

type sniff struct {
	body io.Reader
	read bool
}

func (s *sniff) Read(p []byte) (int, error) {
	s.read = true
	return s.body.Read(p)
}

func (s *sniff) Close() error { return nil }
