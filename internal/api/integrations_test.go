package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/ingress/sig"
)

func TestUnmatchedDeliveryVisibleWithinOneTick(t *testing.T) {
	const secret = "test-webhook-secret"
	const marker = "factory-ready-marker"
	verifier, err := sig.Open("github", secret)
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingVerifier{next: verifier}
	q := newMemQueue("fac_1")
	b := newM4()
	b.queue = q
	b.mounts[ingress.ProviderGitHub] = ingress.New(ingress.Config{
		Provider: ingress.ProviderGitHub,
		Verifier: counter,
		Sink:     q,
	})
	h := NewHandler(b, testAPIKey, nil)

	payload := []byte(`{"action":"labeled","marker":"` + marker + `"}`)
	hdr := map[string]string{
		"X-GitHub-Delivery":   "delivery-1",
		"X-GitHub-Event":      "issues",
		"X-Hub-Signature-256": githubSig(secret, payload),
		"Authorization":       "Bearer should-not-persist",
	}

	res := callBytes(t, h, http.MethodPost, "/api/v1/ingress/github", payload, hdr)
	if res.status != http.StatusOK {
		t.Fatalf("ingress status %d body %s", res.status, res.body)
	}
	var ack struct {
		Duplicate bool `json:"duplicate"`
	}
	if err := json.Unmarshal(res.body, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Duplicate {
		t.Fatal("first delivery was a duplicate")
	}
	if counter.calls() != 1 {
		t.Fatalf("verifier calls %d", counter.calls())
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched", "", apiHeaders())
	before := decode[[]Delivery](t, res, http.StatusOK)
	if len(before) != 0 {
		t.Fatalf("unmatched before dispatch %+v", before)
	}

	dispatcher := &ingress.Dispatcher{
		Store: q,
		Handle: func(context.Context, ingress.Delivery) (bool, error) {
			return false, nil
		},
	}
	n, err := dispatcher.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("claimed %d", n)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched", "", apiHeaders())
	rows := decode[[]Delivery](t, res, http.StatusOK)
	if len(rows) != 1 {
		t.Fatalf("unmatched %+v", rows)
	}
	got := rows[0]
	if got.Provider != ingress.ProviderGitHub || got.Status != ingress.StatusUnmatched || got.DeliveryID != "delivery-1" {
		t.Fatalf("delivery %+v", got)
	}
	if got.ID == "" || !strings.HasPrefix(got.ID, "dlv_") {
		t.Fatalf("id %q", got.ID)
	}
	if len(got.Body) != 0 || bytesContains(res.body, marker) {
		t.Fatalf("body leaked into the default list: %s", res.body)
	}
	if _, ok := got.Headers["Authorization"]; ok {
		t.Fatalf("authorization header persisted: %v", got.Headers)
	}
	if _, ok := got.Headers["X-Hub-Signature-256"]; ok {
		t.Fatalf("signature header persisted: %v", got.Headers)
	}
	if got.Headers["X-Github-Event"] != "issues" || got.Headers["X-Github-Delivery"] != "delivery-1" {
		t.Fatalf("headers %v", got.Headers)
	}
	if !bytesContains(res.body, `"matched_automations":[]`) {
		t.Fatalf("matched automations %s", res.body)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched&include=body", "", apiHeaders())
	withBody := decode[[]Delivery](t, res, http.StatusOK)
	if len(withBody) != 1 || !bytesContains(withBody[0].Body, marker) {
		t.Fatalf("include=body %+v", withBody)
	}

	verified := counter.calls()
	res = call(t, h, http.MethodPost, "/api/v1/deliveries/"+got.ID+":replay?skip_verify=1", "", apiHeaders())
	replayed := decode[Delivery](t, res, http.StatusOK)
	if replayed.Status != ingress.StatusReceived || replayed.ID != got.ID {
		t.Fatalf("replay %+v", replayed)
	}
	if len(replayed.Body) != 0 || bytesContains(res.body, marker) {
		t.Fatalf("replay included the body: %s", res.body)
	}
	if counter.calls() != verified {
		t.Fatalf("replay called the verifier: %d want %d", counter.calls(), verified)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched", "", apiHeaders())
	if rows = decode[[]Delivery](t, res, http.StatusOK); len(rows) != 0 {
		t.Fatalf("still unmatched after replay %+v", rows)
	}
	n, err = dispatcher.Poll(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("second poll n=%d err=%v", n, err)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched", "", apiHeaders())
	if rows = decode[[]Delivery](t, res, http.StatusOK); len(rows) != 1 || rows[0].ID != got.ID {
		t.Fatalf("after second tick %+v", rows)
	}

	res = callBytes(t, h, http.MethodPost, "/api/v1/ingress/github", payload, hdr)
	if res.status != http.StatusOK {
		t.Fatalf("redelivery status %d %s", res.status, res.body)
	}
	if err := json.Unmarshal(res.body, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.Duplicate {
		t.Fatal("redelivery was not a duplicate")
	}
}

func TestIngressRejectsBadSignatureAndOversize(t *testing.T) {
	const secret = "test-webhook-secret"
	verifier, err := sig.Open("github", secret)
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingVerifier{next: verifier}
	q := newMemQueue("fac_1")
	b := newM4()
	b.queue = q
	b.mounts[ingress.ProviderGitHub] = ingress.New(ingress.Config{
		Provider: ingress.ProviderGitHub,
		Verifier: counter,
		Sink:     q,
	})
	var webhookCalls int
	b.webhook = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookCalls++
		if r.PathValue("urlToken") == "" && r.PathValue("webhookUID") == "" {
			t.Errorf("webhook path values missing on %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := NewHandler(b, testAPIKey, nil)

	payload := []byte(`{"action":"opened"}`)
	res := callBytes(t, h, http.MethodPost, "/api/v1/ingress/github", payload, map[string]string{
		"X-GitHub-Delivery":   "delivery-bad",
		"X-Hub-Signature-256": "sha256=0000",
	})
	if res.status != http.StatusUnauthorized {
		t.Fatalf("bad sig status %d body %s", res.status, res.body)
	}
	if counter.calls() != 1 {
		t.Fatalf("verifier calls %d", counter.calls())
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries", "", apiHeaders())
	if rows := decode[[]Delivery](t, res, http.StatusOK); len(rows) != 0 {
		t.Fatalf("failed verify was persisted: %+v", rows)
	}

	big := make([]byte, ingress.MaxBody+1)
	res = callBytes(t, h, http.MethodPost, "/api/v1/ingress/github", big, map[string]string{
		"X-GitHub-Delivery":   "delivery-big",
		"X-Hub-Signature-256": githubSig(secret, big),
	})
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if counter.calls() != 1 {
		t.Fatalf("oversize reached the verifier: %d", counter.calls())
	}

	res = callReader(t, h, http.MethodPost, "/api/v1/webhooks/u/tok", onlyReader{r: bytes.NewReader(big)}, nil)
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversize status %d", res.status)
	}
	if webhookCalls != 0 {
		t.Fatal("oversize webhook reached the handler")
	}

	exact := make([]byte, ingress.MaxBody)
	res = callBytes(t, h, http.MethodPost, "/api/v1/webhooks/whk_1", exact, nil)
	if res.status != http.StatusNoContent || webhookCalls != 1 {
		t.Fatalf("exact webhook status %d calls %d", res.status, webhookCalls)
	}
	res = call(t, h, http.MethodPost, "/api/v1/webhooks/u/tok", "hello", nil)
	if res.status != http.StatusNoContent || webhookCalls != 2 {
		t.Fatalf("url token status %d calls %d", res.status, webhookCalls)
	}

	res = call(t, h, http.MethodGet, "/api/v1/ingress/github", "", nil)
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET ingress status %d", res.status)
	}
	assertProblem(t, res)
}

func TestM4RoutesRequireTheirBackend(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched"},
		{http.MethodPost, "/api/v1/deliveries/dlv_1:replay"},
		{http.MethodGet, "/api/v1/agent/schedules"},
		{http.MethodPost, "/api/v1/agent/schedules"},
		{http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=slack&ref=x"},
		{http.MethodPost, "/api/v1/work-items/wi_1/input"},
		{http.MethodPost, "/api/v1/ingress/slack"},
		{http.MethodPost, "/api/v1/ingress/linear"},
		{http.MethodPost, "/api/v1/webhooks/whk_1"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := call(t, h, tc.method, tc.path, `{"text":"hi"}`, nil)
			if res.status != http.StatusNotFound && res.status != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			// Operator routes require a key before the backend check.
			// Ingress routes do not: an unconfigured provider is 404.
			if strings.Contains(tc.path, "/ingress/") || strings.Contains(tc.path, "/webhooks/") {
				if res.status != http.StatusNotFound {
					t.Fatalf("ingress status %d", res.status)
				}
				return
			}
			if res.status != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", res.status)
			}
		})
	}

	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?include=body", "", nil)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("include=body without a key: %d", res.status)
	}
	assertProblem(t, res)
}

func TestDeliveryListStripsCredentialHeaders(t *testing.T) {
	b := newM4()
	b.listed = []Delivery{{
		ID:         "dlv_poison",
		FactoryID:  "fac_1",
		Provider:   "github",
		DeliveryID: "d1",
		Status:     ingress.StatusUnmatched,
		Headers: map[string]string{
			"Authorization":       "Bearer super-secret",
			"Cookie":              "session=1",
			"X-Hub-Signature-256": "sha256=abcd",
			"X-Github-Event":      "issues",
		},
		Body:               json.RawMessage(`{"action":"labeled"}`),
		MatchedAutomations: nil,
	}}
	h := NewHandler(b, testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=unmatched&include=body", "", apiHeaders())
	if bytesContains(res.body, "super-secret") || bytesContains(res.body, "session=1") || bytesContains(res.body, "sha256=abcd") {
		t.Fatalf("credential header returned: %s", res.body)
	}
	rows := decode[[]Delivery](t, res, http.StatusOK)
	if len(rows) != 1 || rows[0].Headers["X-Github-Event"] != "issues" {
		t.Fatalf("%+v", rows)
	}
	if !bytesContains(rows[0].Body, "labeled") {
		t.Fatalf("body %s", rows[0].Body)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?include=nope", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("include status %d", res.status)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/deliveries?status=nope", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status filter %d", res.status)
	}
	res = call(t, h, http.MethodPost, "/api/v1/deliveries/dlv_poison", "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("replay without suffix %d", res.status)
	}
}

// TestDeliveryNormalizedEventsWire checks the bytes writeJSON emits.
// SQL NULL omits normalized_events. A stored [] encodes as [].
// include=body changes only the body key.
func TestDeliveryNormalizedEventsWire(t *testing.T) {
	event := json.RawMessage(`{"event":"received","provider":"webhook"}`)
	payload := json.RawMessage(`{"ok":true}`)
	when := time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC)
	row := func(id string, events []json.RawMessage) Delivery {
		return Delivery{
			ID: id, FactoryID: "fac_1", Provider: "webhook", DeliveryID: id,
			ReceivedAt: when, Status: ingress.StatusReceived, Body: payload,
			NormalizedEvents: events,
		}
	}
	b := newM4()
	b.listed = []Delivery{
		row("del_null", nil),
		row("del_empty", []json.RawMessage{}),
		row("del_event", []json.RawMessage{event}),
	}
	h := NewHandler(b, testAPIKey, nil)
	for _, include := range []bool{false, true} {
		name := "without body"
		path := "/api/v1/factory/fac_1/deliveries"
		if include {
			name = "with body"
			path += "?include=body"
		}
		t.Run(name, func(t *testing.T) {
			res := call(t, h, http.MethodGet, path, "", apiHeaders())
			raws := decode[[]json.RawMessage](t, res, http.StatusOK)
			if len(raws) != 3 {
				t.Fatalf("rows %d %s", len(raws), res.body)
			}
			if strings.Contains(string(raws[0]), "normalized_events") {
				t.Fatalf("null column kept the key: %s", raws[0])
			}
			if !bytes.Contains(raws[1], []byte(`"normalized_events":[]`)) {
				t.Fatalf("empty list omitted: %s", raws[1])
			}
			if !bytes.Contains(raws[2], []byte(`"normalized_events":[{"event":"received","provider":"webhook"}]`)) {
				t.Fatalf("events %s", raws[2])
			}
			rows := decode[[]Delivery](t, res, http.StatusOK)
			if rows[0].NormalizedEvents != nil || rows[1].NormalizedEvents == nil || len(rows[1].NormalizedEvents) != 0 || len(rows[2].NormalizedEvents) != 1 {
				t.Fatalf("decoded %+v", rows)
			}
			for i, raw := range raws {
				hasBody := bytes.Contains(raw, []byte(`"body":{"ok":true}`))
				if hasBody != include {
					t.Fatalf("row %d include=%v body %s", i, include, raw)
				}
			}
		})
	}
}

func TestSchedulesCRUDPauseAndResume(t *testing.T) {
	b := newM4()
	h := NewHandler(b, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/agent/schedules", `{"factory_id":"fac_1","automation":"audit","name":"weekly","cron":"0 6 * * 1"}`, apiHeaders())
	created := decode[Schedule](t, res, http.StatusCreated)
	if created.ID == "" || created.Paused || created.Cron != "0 6 * * 1" || created.FactoryID != "fac_1" {
		t.Fatalf("%+v", created)
	}

	res = call(t, h, http.MethodPost, "/api/v1/agent/schedules", `{"factory_id":"fac_1","automation":"audit","name":"weekly","cron":"@daily"}`, apiHeaders())
	if res.status != http.StatusConflict {
		t.Fatalf("duplicate status %d %s", res.status, res.body)
	}

	res = call(t, h, http.MethodPost, "/api/v1/agent/schedules", `{"factory_id":"fac_1","automation":"audit","name":"fast","cron":"@every 30s"}`, apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("short every status %d", res.status)
	}

	res = call(t, h, http.MethodGet, "/api/v1/agent/schedules?factory=fac_1", "", apiHeaders())
	rows := decode[[]Schedule](t, res, http.StatusOK)
	if len(rows) != 1 || rows[0].ID != created.ID {
		t.Fatalf("%+v", rows)
	}

	res = call(t, h, http.MethodPatch, "/api/v1/agent/schedules/"+created.ID, `{"cron":"@daily"}`, apiHeaders())
	patched := decode[Schedule](t, res, http.StatusOK)
	if patched.Cron != "@daily" || patched.Name != "weekly" {
		t.Fatalf("%+v", patched)
	}

	res = call(t, h, http.MethodPost, "/api/v1/agent/schedules/"+created.ID+"/pause", "", apiHeaders())
	paused := decode[Schedule](t, res, http.StatusOK)
	if !paused.Paused {
		t.Fatal("not paused")
	}
	res = call(t, h, http.MethodPost, "/api/v1/agent/schedules/"+created.ID+"/resume", "", apiHeaders())
	resumed := decode[Schedule](t, res, http.StatusOK)
	if resumed.Paused {
		t.Fatal("still paused")
	}

	res = call(t, h, http.MethodDelete, "/api/v1/agent/schedules/"+created.ID, "", apiHeaders())
	if res.status != http.StatusNoContent {
		t.Fatalf("delete %d %s", res.status, res.body)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/schedules/"+created.ID, "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("get after delete %d", res.status)
	}
}

func TestRunByExternalReferenceAndAnswer(t *testing.T) {
	b := newM4()
	run := Run{ID: "run_1", Status: "running", ConversationID: "conv_1", WorkItemID: "wi_1"}
	b.refs["slack\x00slack:T/C/1.0"] = ExternalRun{
		Provider:       "slack",
		ExternalRef:    "slack:T/C/1.0",
		WorkItemID:     "wi_1",
		ConversationID: "conv_1",
		Run:            &run,
	}
	b.open["wi_1"] = true
	h := NewHandler(b, testAPIKey, nil)

	res := call(t, h, http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=slack&ref=slack:T/C/1.0", "", apiHeaders())
	got := decode[ExternalRun](t, res, http.StatusOK)
	if got.WorkItemID != "wi_1" || got.Run == nil || got.Run.ID != "run_1" || got.ConversationID != "conv_1" {
		t.Fatalf("%+v", got)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=slack", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("missing ref %d", res.status)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=github&ref=missing", "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("missing binding %d", res.status)
	}

	res = call(t, h, http.MethodPost, "/api/v1/work-items/wi_1/input", `{"text":"ship it","answered_by":"attacker"}`, apiHeaders())
	ans := decode[InputAnswer](t, res, http.StatusOK)
	if ans.Answer != "ship it" || ans.AnsweredBy != APIKeyPrincipal || ans.WorkItemID != "wi_1" {
		t.Fatalf("%+v", ans)
	}
	res = call(t, h, http.MethodPost, "/api/v1/work-items/wi_1/input", `{"text":"ship it"}`, apiHeaders())
	again := decode[InputAnswer](t, res, http.StatusOK)
	if again.ID != ans.ID {
		t.Fatalf("idempotent answer changed id %s %s", again.ID, ans.ID)
	}
	res = call(t, h, http.MethodPost, "/api/v1/work-items/wi_1/input", `{"text":"different"}`, apiHeaders())
	if res.status != http.StatusConflict {
		t.Fatalf("conflict %d %s", res.status, res.body)
	}
	res = call(t, h, http.MethodPost, "/api/v1/work-items/wi_missing/input", `{"text":"x"}`, apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("missing work item %d", res.status)
	}
	res = call(t, h, http.MethodPost, "/api/v1/work-items/wi_1/input", `{"text":"  "}`, apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("blank text %d", res.status)
	}
}

func TestIngressProvidersMountWithoutAPIKey(t *testing.T) {
	b := newM4()
	var seen []string
	for _, provider := range []string{ingress.ProviderGitHub, ingress.ProviderSlack, ingress.ProviderLinear} {
		b.mounts[provider] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			seen = append(seen, provider)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	h := NewHandler(b, testAPIKey, nil)
	for _, provider := range []string{ingress.ProviderGitHub, ingress.ProviderSlack, ingress.ProviderLinear} {
		res := call(t, h, http.MethodPost, "/api/v1/ingress/"+provider, `{}`, map[string]string{
			"Authorization": "Bearer not-the-api-key",
		})
		if res.status != http.StatusNoContent {
			t.Fatalf("%s status %d body %s", provider, res.status, res.body)
		}
	}
	if strings.Join(seen, ",") != "github,slack,linear" {
		t.Fatalf("mounted %v", seen)
	}
}

type m4Backend struct {
	*fakeBackend
	mu        sync.Mutex
	queue     *memQueue
	listed    []Delivery
	schedules map[string]Schedule
	schedSeq  int
	refs      map[string]ExternalRun
	answers   map[string]InputAnswer
	answerSeq int
	open      map[string]bool
	mounts    map[string]http.Handler
	webhook   http.Handler
}

func newM4() *m4Backend {
	return &m4Backend{
		fakeBackend: newFake(),
		schedules:   map[string]Schedule{},
		refs:        map[string]ExternalRun{},
		answers:     map[string]InputAnswer{},
		open:        map[string]bool{},
		mounts:      map[string]http.Handler{},
	}
}

func (b *m4Backend) ListDeliveries(ctx context.Context, factoryID, status string, limit int) ([]Delivery, error) {
	if err := b.blocked(ctx); err != nil {
		return nil, err
	}
	if _, err := b.GetFactory(ctx, factoryID); err != nil {
		return nil, err
	}
	if b.queue != nil {
		return b.queue.list(factoryID, status, limit), nil
	}
	if limit <= 0 {
		limit = 100
	}
	out := []Delivery{}
	for _, row := range b.listed {
		if row.FactoryID != "" && row.FactoryID != factoryID {
			continue
		}
		if status != "" && row.Status != status {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (b *m4Backend) ReplayDelivery(_ context.Context, id string) (Delivery, error) {
	if b.queue == nil {
		return Delivery{}, ErrNotFound
	}
	return b.queue.replay(id)
}

func (b *m4Backend) ListSchedules(ctx context.Context, factoryID string) ([]Schedule, error) {
	if err := b.blocked(ctx); err != nil {
		return nil, err
	}
	if factoryID != "" {
		if _, err := b.GetFactory(ctx, factoryID); err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Schedule{}
	for _, row := range b.schedules {
		if factoryID != "" && row.FactoryID != factoryID {
			continue
		}
		out = append(out, row)
	}
	sortSchedules(out)
	return out, nil
}

func (b *m4Backend) GetSchedule(_ context.Context, id string) (Schedule, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.scheduleLocked(id)
}

func (b *m4Backend) CreateSchedule(ctx context.Context, req ScheduleRequest) (Schedule, error) {
	if _, err := b.GetFactory(ctx, req.FactoryID); err != nil {
		return Schedule{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, row := range b.schedules {
		if row.FactoryID == req.FactoryID && row.Automation == req.Automation && row.Name == req.Name {
			return Schedule{}, ErrConflict
		}
	}
	b.schedSeq++
	row := Schedule{
		ID:         "sch_" + strconv.Itoa(b.schedSeq),
		FactoryID:  req.FactoryID,
		Automation: req.Automation,
		Name:       req.Name,
		Cron:       req.Cron,
		NextFireAt: time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC),
		Paused:     req.Paused,
	}
	b.schedules[row.ID] = row
	return row, nil
}

func (b *m4Backend) UpdateSchedule(_ context.Context, id string, patch SchedulePatch) (Schedule, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	row, err := b.scheduleLocked(id)
	if err != nil {
		return Schedule{}, err
	}
	if patch.Automation != nil {
		row.Automation = *patch.Automation
	}
	if patch.Name != nil {
		row.Name = *patch.Name
	}
	if patch.Cron != nil {
		row.Cron = *patch.Cron
	}
	if patch.Paused != nil {
		row.Paused = *patch.Paused
	}
	for otherID, other := range b.schedules {
		if otherID == id {
			continue
		}
		if other.FactoryID == row.FactoryID && other.Automation == row.Automation && other.Name == row.Name {
			return Schedule{}, ErrConflict
		}
	}
	b.schedules[id] = row
	return row, nil
}

func (b *m4Backend) DeleteSchedule(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.schedules[id]; !ok {
		return ErrNotFound
	}
	delete(b.schedules, id)
	return nil
}

func (b *m4Backend) SetSchedulePaused(_ context.Context, id string, paused bool) (Schedule, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	row, err := b.scheduleLocked(id)
	if err != nil {
		return Schedule{}, err
	}
	row.Paused = paused
	b.schedules[id] = row
	return row, nil
}

func (b *m4Backend) scheduleLocked(id string) (Schedule, error) {
	row, ok := b.schedules[id]
	if !ok {
		return Schedule{}, ErrNotFound
	}
	return row, nil
}

func (b *m4Backend) RunByExternalReference(_ context.Context, provider, ref string) (ExternalRun, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	row, ok := b.refs[provider+"\x00"+ref]
	if !ok {
		return ExternalRun{}, ErrNotFound
	}
	return row, nil
}

func (b *m4Backend) AnswerInput(_ context.Context, workItemID, text, answeredBy string) (InputAnswer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev, ok := b.answers[workItemID]; ok {
		if prev.Answer == text {
			return prev, nil
		}
		return InputAnswer{}, ErrConflict
	}
	if !b.open[workItemID] {
		return InputAnswer{}, ErrNotFound
	}
	b.answerSeq++
	row := InputAnswer{
		ID:         "inr_" + strconv.Itoa(b.answerSeq),
		WorkItemID: workItemID,
		Answer:     text,
		AnsweredBy: answeredBy,
	}
	b.answers[workItemID] = row
	return row, nil
}

func (b *m4Backend) MountIngress(provider string) (http.Handler, bool) {
	h, ok := b.mounts[provider]
	if !ok || h == nil {
		return nil, false
	}
	return h, true
}

func (b *m4Backend) MountWebhook() (http.Handler, bool) {
	if b.webhook == nil {
		return nil, false
	}
	return b.webhook, true
}

func sortSchedules(rows []Schedule) {
	for i := 1; i < len(rows); i++ {
		j := i
		for j > 0 && scheduleLess(rows[j], rows[j-1]) {
			rows[j], rows[j-1] = rows[j-1], rows[j]
			j--
		}
	}
}

func scheduleLess(a, b Schedule) bool {
	if a.Automation != b.Automation {
		return a.Automation < b.Automation
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

type memQueue struct {
	mu      sync.Mutex
	factory string
	rows    []Delivery
	held    map[string]bool
}

func newMemQueue(factory string) *memQueue {
	return &memQueue{factory: factory, held: map[string]bool{}}
}

func (q *memQueue) Enqueue(_ context.Context, d ingress.Delivery) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.Provider == d.Provider && row.WebhookUID == d.WebhookUID && row.DeliveryID == d.DeliveryID {
			return true, nil
		}
	}
	headers := make(map[string]string, len(d.Headers))
	for k, v := range d.Headers {
		headers[k] = v
	}
	q.rows = append(q.rows, Delivery{
		ID:                 d.ID,
		FactoryID:          q.factory,
		Provider:           d.Provider,
		WebhookUID:         d.WebhookUID,
		DeliveryID:         d.DeliveryID,
		ReceivedAt:         d.ReceivedAt,
		Headers:            headers,
		Body:               append(json.RawMessage(nil), d.Body...),
		Truncated:          d.Truncated,
		Status:             ingress.StatusReceived,
		MatchedAutomations: []string{},
	})
	return false, nil
}

func (q *memQueue) ClaimReceived(_ context.Context, limit int) ([]ingress.Delivery, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		return nil, nil
	}
	out := []ingress.Delivery{}
	for i := range q.rows {
		row := &q.rows[i]
		if row.Status != ingress.StatusReceived || q.held[row.ID] {
			continue
		}
		q.held[row.ID] = true
		out = append(out, ingress.Delivery{
			ID:         row.ID,
			Provider:   row.Provider,
			WebhookUID: row.WebhookUID,
			DeliveryID: row.DeliveryID,
			ReceivedAt: row.ReceivedAt,
			Headers:    row.Headers,
			Body:       append([]byte(nil), row.Body...),
			Truncated:  row.Truncated,
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (q *memQueue) MarkDelivery(_ context.Context, id, status string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.rows {
		if q.rows[i].ID != id {
			continue
		}
		q.rows[i].Status = status
		delete(q.held, id)
		return nil
	}
	return ErrNotFound
}

func (q *memQueue) list(factory, status string, limit int) []Delivery {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := []Delivery{}
	for i := len(q.rows) - 1; i >= 0; i-- {
		row := q.rows[i]
		if row.FactoryID != factory {
			continue
		}
		if status != "" && row.Status != status {
			continue
		}
		row.Headers = cloneHeaders(row.Headers)
		row.Body = append(json.RawMessage(nil), row.Body...)
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (q *memQueue) replay(id string) (Delivery, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.rows {
		if q.rows[i].ID != id {
			continue
		}
		q.rows[i].Status = ingress.StatusReceived
		delete(q.held, id)
		row := q.rows[i]
		row.Headers = cloneHeaders(row.Headers)
		row.Body = append(json.RawMessage(nil), row.Body...)
		return row, nil
	}
	return Delivery{}, ErrNotFound
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type countingVerifier struct {
	mu   sync.Mutex
	next ingress.Verifier
	n    int
}

func (v *countingVerifier) Verify(h http.Header, body []byte, now time.Time) error {
	v.mu.Lock()
	v.n++
	v.mu.Unlock()
	return v.next.Verify(h, body, now)
}

func (v *countingVerifier) calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.n
}

type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func githubSig(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func bytesContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}
