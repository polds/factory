package operator

import (
	"context"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCertRotation(t *testing.T) {
	ctx := context.Background()
	ns := "op"
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)
	cfg := &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "factory-validating"},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name: "factory.factory.dev",
			ClientConfig: admissionv1.WebhookClientConfig{
				CABundle: []byte("placeholder"),
			},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(Scheme).WithObjects(cfg).Build()
	issuer := &Issuer{
		Client:       c,
		Namespace:    ns,
		SecretName:   "webhook",
		WebhookName:  cfg.Name,
		DNSNames:     []string{"factory-operator-webhook"},
		Clock:        clock,
		LeafTTL:      2 * time.Hour,
		RotateBefore: time.Hour,
	}
	if err := issuer.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "webhook"}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data[secretCert]) == 0 || len(secret.Data[secretKey]) == 0 || len(secret.Data[secretCA]) == 0 {
		t.Fatal("serving secret is incomplete")
	}
	var patched admissionv1.ValidatingWebhookConfiguration
	if err := c.Get(ctx, types.NamespacedName{Name: cfg.Name}, &patched); err != nil {
		t.Fatal(err)
	}
	first := string(patched.Webhooks[0].ClientConfig.CABundle)
	if first == "placeholder" || first != string(secret.Data[secretCA]) {
		t.Fatal("caBundle was not patched from the secret CA")
	}
	clock.Advance(30 * time.Minute)
	if err := issuer.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: cfg.Name}, &patched); err != nil {
		t.Fatal(err)
	}
	if string(patched.Webhooks[0].ClientConfig.CABundle) != first {
		t.Fatal("certificate rotated inside the leaf lifetime")
	}
	clock.Advance(31 * time.Minute)
	if err := issuer.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: cfg.Name}, &patched); err != nil {
		t.Fatal(err)
	}
	if string(patched.Webhooks[0].ClientConfig.CABundle) == first {
		t.Fatal("certificate did not rotate before expiry")
	}
}
