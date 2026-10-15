package webhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestApplyPreservesUIDAndUpdatesMutableFields(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{rows: []store.WebhookRegistration{{
		UID:              "whk_ci",
		FactoryID:        "fac_1",
		Name:             "ci",
		AuthMode:         store.WebhookAuthToken,
		SecretRef:        "CI_WEBHOOK_SECRET",
		DeliveryIDHeader: "X-CI-Run-Id",
		Enabled:          true,
	}}}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{{
		Name:             "ci",
		AuthMode:         store.WebhookAuthToken,
		SecretName:       "CI_WEBHOOK_SECRET_ROTATED",
		DeliveryIDHeader: "X-Request-Id",
		Enabled:          false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncer.applied) != 1 {
		t.Fatalf("applied %d", len(syncer.applied))
	}
	got := syncer.applied[0]
	if got.UID != "whk_ci" || got.AuthMode != store.WebhookAuthToken || got.Enabled || got.SecretRef != "CI_WEBHOOK_SECRET_ROTATED" || got.DeliveryIDHeader != "X-Request-Id" {
		t.Fatalf("%+v", got)
	}
	if len(got.URLTokenHash) != 0 {
		t.Fatalf("token mode stored a url hash")
	}
}

func TestApplyImmutabilityNamesTheRecreateRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		next Spec
	}{
		{name: "auth mode", next: Spec{Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET"}},
		{name: "signature scheme", next: Spec{Name: "ci", AuthMode: store.WebhookAuthSignature, SignatureScheme: "gitlab", SecretName: "CI_WEBHOOK_SECRET"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			syncer := &fakeSync{rows: []store.WebhookRegistration{{
				UID:             "whk_ci",
				FactoryID:       "fac_1",
				Name:            "ci",
				AuthMode:        store.WebhookAuthSignature,
				SignatureScheme: "github",
				SecretRef:       "CI_WEBHOOK_SECRET",
				Enabled:         true,
			}}}
			err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{tc.next})
			if err == nil || !errors.Is(err, store.ErrImmutable) || !strings.Contains(err.Error(), ImmutableRule) {
				t.Fatalf("err %v", err)
			}
			if syncer.applyCalls != 0 {
				t.Fatalf("apply calls %d", syncer.applyCalls)
			}
			if syncer.rows[0].AuthMode != store.WebhookAuthSignature || syncer.rows[0].SignatureScheme != "github" {
				t.Fatalf("row changed %+v", syncer.rows[0])
			}
		})
	}
}

func TestApplyStoreImmutableRace(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{applyErr: store.ErrImmutable}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err == nil || !errors.Is(err, store.ErrImmutable) || !strings.Contains(err.Error(), ImmutableRule) {
		t.Fatalf("err %v", err)
	}
}

func TestApplyURLTokenHashesSecretAndOmitsIt(t *testing.T) {
	t.Parallel()
	const secret = "url-token-z9q.ABC_~"
	secrets := &memSecrets{values: map[string]string{"CI_WEBHOOK_SECRET": secret}}
	syncer := &fakeSync{}
	err := Apply(context.Background(), syncer, secrets, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthURLToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := syncer.applied[0]
	sum := sha256.Sum256([]byte(secret))
	if !bytes.Equal(got.URLTokenHash, sum[:]) {
		t.Fatalf("hash %x", got.URLTokenHash)
	}
	if got.SecretRef != "CI_WEBHOOK_SECRET" || got.UID != "" {
		t.Fatalf("%+v", got)
	}
	blob := got.SecretRef + got.Name + got.AuthMode + string(got.URLTokenHash)
	if strings.Contains(blob, secret) || bytes.Contains(got.URLTokenHash, []byte(secret)) {
		t.Fatal("secret stored")
	}
	if secrets.calls[0] != "CI_WEBHOOK_SECRET" {
		t.Fatalf("calls %v", secrets.calls)
	}
}

func TestApplyDisabledURLTokenWithoutSecret(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{err: errors.New("missing")}
	syncer := &fakeSync{rows: []store.WebhookRegistration{{
		UID: "whk_ci", FactoryID: "fac_1", Name: "ci",
		AuthMode: store.WebhookAuthURLToken, SecretRef: "CI_WEBHOOK_SECRET",
		URLTokenHash: bytes.Repeat([]byte{1}, 32),
	}}}
	err := Apply(context.Background(), syncer, secrets, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthURLToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(syncer.applied[0].URLTokenHash, bytes.Repeat([]byte{1}, 32)) || syncer.applied[0].Enabled {
		t.Fatalf("%+v", syncer.applied[0])
	}
}

func TestApplyEnabledURLTokenRequiresSecret(t *testing.T) {
	t.Parallel()
	const secret = "url-token-z9q.ABC_~"
	secrets := &memSecrets{err: errors.New("missing " + secret)}
	syncer := &fakeSync{}
	err := Apply(context.Background(), syncer, secrets, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthURLToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err == nil || strings.Contains(err.Error(), secret) || syncer.applyCalls != 0 {
		t.Fatalf("err %v calls %d", err, syncer.applyCalls)
	}
	if !strings.Contains(err.Error(), "CI_WEBHOOK_SECRET") {
		t.Fatal(err)
	}
}

func TestApplyRejectsBadURLTokenCharset(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{"CI_WEBHOOK_SECRET": "has space"}}
	syncer := &fakeSync{}
	err := Apply(context.Background(), syncer, secrets, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthURLToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: false,
	}})
	if err == nil || !strings.Contains(err.Error(), "A-Za-z0-9-._~") || strings.Contains(err.Error(), "has space") {
		t.Fatalf("err %v", err)
	}
}

func TestApplyHeaderRejection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
	}{
		{name: "authorization", header: "Authorization"},
		{name: "cookie", header: "Cookie"},
		{name: "github signature", header: "X-Hub-Signature-256"},
		{name: "stripe signature", header: "Stripe-Signature"},
		{name: "standard signature", header: "Webhook-Signature"},
		{name: "sentry signature", header: "Sentry-Hook-Signature"},
		{name: "pagerduty signature", header: "X-PagerDuty-Signature"},
		{name: "vercel signature", header: "X-Vercel-Signature"},
		{name: "too long", header: strings.Repeat("A", 65)},
		{name: "space", header: "Bad Name"},
		{name: "colon", header: "X-CI:Run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			syncer := &fakeSync{}
			err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{{
				Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET",
				DeliveryIDHeader: tc.header, Enabled: true,
			}})
			if err == nil || syncer.applyCalls != 0 {
				t.Fatalf("err %v calls %d", err, syncer.applyCalls)
			}
		})
	}
}

func TestApplyAcceptsEmptyAndOrdinaryHeader(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{
		{Name: "bare", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true},
		{Name: "headed", AuthMode: store.WebhookAuthSignature, SignatureScheme: "github", SecretName: "CI_WEBHOOK_SECRET", DeliveryIDHeader: "X-CI-Run-Id", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncer.applied) != 2 || syncer.applied[0].Name != "bare" || syncer.applied[1].DeliveryIDHeader != "X-CI-Run-Id" {
		t.Fatalf("%+v", syncer.applied)
	}
}

func TestApplyDeletesOmittedNames(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{rows: []store.WebhookRegistration{
		{UID: "whk_old", FactoryID: "fac_1", Name: "old", AuthMode: store.WebhookAuthToken, SecretRef: "CI_WEBHOOK_SECRET"},
		{UID: "whk_keep", FactoryID: "fac_1", Name: "keep", AuthMode: store.WebhookAuthToken, SecretRef: "CI_WEBHOOK_SECRET"},
	}}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{{
		Name: "keep", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncer.applied) != 1 || syncer.applied[0].UID != "whk_keep" {
		t.Fatalf("%+v", syncer.applied)
	}
}

func TestApplyRenameIsCreateUnderNewName(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{rows: []store.WebhookRegistration{{
		UID: "whk_ci", FactoryID: "fac_1", Name: "ci",
		AuthMode: store.WebhookAuthSignature, SignatureScheme: "github", SecretRef: "CI_WEBHOOK_SECRET",
	}}}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{{
		Name: "ci-v2", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(syncer.applied) != 1 || syncer.applied[0].Name != "ci-v2" || syncer.applied[0].UID != "" || syncer.applied[0].AuthMode != store.WebhookAuthToken {
		t.Fatalf("%+v", syncer.applied[0])
	}
}

func TestApplyRejectsDuplicateAndBadShape(t *testing.T) {
	t.Parallel()
	syncer := &fakeSync{}
	cases := []Spec{
		{Name: "ci", AuthMode: "nope", SecretName: "CI_WEBHOOK_SECRET"},
		{Name: "ci", AuthMode: store.WebhookAuthToken, SignatureScheme: "github", SecretName: "CI_WEBHOOK_SECRET"},
		{Name: "ci", AuthMode: store.WebhookAuthSignature, SecretName: "CI_WEBHOOK_SECRET"},
		{Name: "ci", AuthMode: store.WebhookAuthSignature, SignatureScheme: "slack", SecretName: "CI_WEBHOOK_SECRET"},
		{Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "not-a-name"},
		{Name: "", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET"},
	}
	for _, spec := range cases {
		if err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{spec}); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
	err := Apply(context.Background(), syncer, nil, "fac_1", []Spec{
		{Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET"},
		{Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), syncer, nil, " ", nil); err == nil {
		t.Fatal("empty factory")
	}
	if syncer.applyCalls != 0 {
		t.Fatalf("apply calls %d", syncer.applyCalls)
	}
}

func TestSpecFromDefinition(t *testing.T) {
	t.Parallel()
	got := SpecFromDefinition(definition.Webhook{
		Name: "ci", AuthMode: "token", SecretName: "CI_WEBHOOK_SECRET",
		DeliveryIDHeader: "X-CI-Run-Id", Enabled: true,
	})
	if got.Name != "ci" || got.AuthMode != "token" || !got.Enabled || got.DeliveryIDHeader != "X-CI-Run-Id" {
		t.Fatalf("%+v", got)
	}
}

func TestTokenModeDoesNotResolveSecret(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{err: errors.New("should not be called")}
	syncer := &fakeSync{}
	err := Apply(context.Background(), syncer, secrets, "fac_1", []Spec{{
		Name: "ci", AuthMode: store.WebhookAuthToken, SecretName: "CI_WEBHOOK_SECRET", Enabled: true,
	}})
	if err != nil || len(secrets.calls) != 0 {
		t.Fatalf("err %v calls %v", err, secrets.calls)
	}
}

type fakeSync struct {
	rows       []store.WebhookRegistration
	applied    []store.WebhookRegistration
	applyCalls int
	applyErr   error
	listErr    error
}

func (f *fakeSync) ListWebhookRegistrations(context.Context, string) ([]store.WebhookRegistration, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]store.WebhookRegistration(nil), f.rows...), nil
}

func (f *fakeSync) ApplyWebhooks(_ context.Context, _ string, regs []store.WebhookRegistration) error {
	f.applyCalls++
	f.applied = regs
	if f.applyErr != nil {
		return f.applyErr
	}
	f.rows = append([]store.WebhookRegistration(nil), regs...)
	return nil
}

type memSecrets struct {
	mu     sync.Mutex
	values map[string]string
	calls  []string
	err    error
}

func (m *memSecrets) Secret(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, name)
	if m.err != nil {
		return "", m.err
	}
	value, ok := m.values[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}
