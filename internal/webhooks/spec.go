package webhooks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

// ImmutableRule is the only way to change authMode or signatureScheme:
// delete the webhook and create another under a new name.
const ImmutableRule = "delete the webhook and recreate it under a new name"

// Spec is one definition webhook. SecretName is a credential name, never
// the secret value.
type Spec struct {
	Name             string
	AuthMode         string
	SignatureScheme  string
	SecretName       string
	DeliveryIDHeader string
	Enabled          bool
}

// SpecFromDefinition copies a loaded webhook. Name is the definition key.
func SpecFromDefinition(w definition.Webhook) Spec {
	return Spec{
		Name:             w.Name,
		AuthMode:         w.AuthMode,
		SignatureScheme:  w.SignatureScheme,
		SecretName:       w.SecretName,
		DeliveryIDHeader: w.DeliveryIDHeader,
		Enabled:          w.Enabled,
	}
}

type authMode string

const (
	authToken     authMode = store.WebhookAuthToken
	authURLToken  authMode = store.WebhookAuthURLToken
	authSignature authMode = store.WebhookAuthSignature
)

func neverAuth(m authMode) authMode { return m }

type scheme string

const (
	schemeGitHub    scheme = "github"
	schemeGitLab    scheme = "gitlab"
	schemeStripe    scheme = "stripe"
	schemeSentry    scheme = "sentry"
	schemePagerDuty scheme = "pagerduty"
	schemeVercel    scheme = "vercel"
	schemeStandard  scheme = "standard_webhooks"
)

func neverScheme(s scheme) scheme { return s }

var (
	secretNameExact = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	headerNameExact = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)
)

func normalizeSpec(in Spec) (Spec, error) {
	spec := Spec{
		Name:             strings.TrimSpace(in.Name),
		AuthMode:         strings.TrimSpace(in.AuthMode),
		SignatureScheme:  strings.TrimSpace(in.SignatureScheme),
		SecretName:       strings.TrimSpace(in.SecretName),
		DeliveryIDHeader: strings.TrimSpace(in.DeliveryIDHeader),
		Enabled:          in.Enabled,
	}
	if spec.Name == "" {
		return Spec{}, fmt.Errorf("webhooks: webhook name is required")
	}
	switch authMode(spec.AuthMode) {
	case authToken, authURLToken, authSignature:
	default:
		return Spec{}, fmt.Errorf("webhooks: unsupported authMode %q", string(neverAuth(authMode(spec.AuthMode))))
	}
	if err := validateScheme(authMode(spec.AuthMode), spec.SignatureScheme); err != nil {
		return Spec{}, err
	}
	if !secretNameExact.MatchString(spec.SecretName) {
		return Spec{}, fmt.Errorf("webhooks: webhook %q: secretName must name a secret", spec.Name)
	}
	if err := ValidateDeliveryIDHeader(spec.DeliveryIDHeader); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func validateScheme(mode authMode, name string) error {
	switch mode {
	case authToken, authURLToken:
		if name != "" {
			return fmt.Errorf("webhooks: signatureScheme is only valid when authMode is signature")
		}
		return nil
	case authSignature:
		if name == "" {
			return fmt.Errorf("webhooks: signatureScheme is required when authMode is signature")
		}
		switch scheme(name) {
		case schemeGitHub, schemeGitLab, schemeStripe, schemeSentry, schemePagerDuty, schemeVercel, schemeStandard:
			return nil
		default:
			return fmt.Errorf("webhooks: unsupported signatureScheme %q", string(neverScheme(scheme(name))))
		}
	default:
		return fmt.Errorf("webhooks: unsupported authMode %q", string(neverAuth(mode)))
	}
}

// ValidateDeliveryIDHeader accepts an empty name. A set name is at most 64
// characters, a token per RFC 9110, and not Authorization, Cookie, or any
// header whose name contains "signature".
func ValidateDeliveryIDHeader(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 64 {
		return fmt.Errorf("webhooks: deliveryIdHeader is longer than 64 characters")
	}
	if !headerNameExact.MatchString(name) {
		return fmt.Errorf("webhooks: deliveryIdHeader %q is not a valid header name", name)
	}
	if rejectedDeliveryHeader(name) {
		return fmt.Errorf("webhooks: deliveryIdHeader %q cannot carry credentials", name)
	}
	return nil
}

func rejectedDeliveryHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie":
		return true
	default:
		return strings.Contains(strings.ToLower(name), "signature")
	}
}
