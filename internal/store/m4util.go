package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func jsonObject(headers map[string]string) ([]byte, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("store: json: %w", err)
	}
	return raw, nil
}

func jsonStringList(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("store: json: %w", err)
	}
	return raw, nil
}

func decodeObject(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("store: json: %w", err)
	}
	return out, nil
}

func decodeStringList(raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("store: json: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func immutableOr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.Contains(pgErr.Message, "immutable") {
		return ErrImmutable
	}
	return err
}

// uniqueOn reports a unique or primary-key violation on table.
func uniqueOn(err error, table string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	if pgErr.TableName == table {
		return true
	}
	return strings.HasPrefix(pgErr.ConstraintName, table+"_")
}

func validProvider(provider string) error {
	switch provider {
	case ProviderGitHub, ProviderSlack, ProviderLinear, ProviderWebhook, ProviderSchedule:
		return nil
	default:
		return fmt.Errorf("store: provider %q is not valid", provider)
	}
}

func validDeliveryStatus(status string) error {
	switch status {
	case DeliveryReceived, DeliveryMatched, DeliveryUnmatched, DeliveryIgnored, DeliveryFailed:
		return nil
	default:
		return fmt.Errorf("store: delivery status %q is not valid", status)
	}
}

func validWebhookAuth(mode string) error {
	switch mode {
	case WebhookAuthToken, WebhookAuthURLToken, WebhookAuthSignature:
		return nil
	default:
		return fmt.Errorf("store: webhook auth mode %q is not valid", mode)
	}
}

// credentialHeader reports headers that must not be stored.
// Signature headers, Authorization, and Cookie are stripped.
func credentialHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "authorization", "proxy-authorization", "cookie", "x-gitlab-token":
		return true
	default:
		return strings.Contains(n, "signature") || strings.Contains(n, "secret")
	}
}

func redactHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		if credentialHeader(key) {
			continue
		}
		out[key] = value
	}
	return out
}

func capBody(body []byte, truncated bool) ([]byte, bool) {
	if len(body) > DeliveryBodyCap {
		clipped := make([]byte, DeliveryBodyCap)
		copy(clipped, body)
		return clipped, true
	}
	if body == nil {
		return []byte{}, truncated
	}
	return body, truncated
}
