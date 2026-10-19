package inference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

const planted = "sk-planted-extra-header-value"

func TestParseExtraHeaders(t *testing.T) {
	raw := "x-litellm-api-key: Bearer " + planted + "\nAuthorization: Bearer oauth-planted-token\n"
	got, err := ParseExtraHeaders(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "x-litellm-api-key" || got[1].Name != "Authorization" {
		t.Fatalf("names %#v", got)
	}
	if string(got[0].value) != "Bearer "+planted || string(got[1].value) != "Bearer oauth-planted-token" {
		t.Fatal("parsed values mismatch")
	}
	again, err := ParseExtraHeaders(Format(got))
	if err != nil {
		t.Fatal(err)
	}
	if Format(again) != Format(got) {
		t.Fatal("format did not round-trip")
	}

	last, err := ParseExtraHeaders("X-Litellm-Api-Key: first-token-value\nx-litellm-api-key: second-token-value")
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 || last[0].Name != "x-litellm-api-key" || string(last[0].value) != "second-token-value" {
		t.Fatalf("last duplicate %#v", last)
	}

	empty, err := ParseExtraHeaders(" \n\t")
	if err != nil || empty != nil {
		t.Fatalf("empty: %v %#v", err, empty)
	}
	none, err := ParseExtraHeaders("")
	if err != nil || none != nil {
		t.Fatalf("unset: %v %#v", err, none)
	}
}

func TestParseExtraHeadersRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "crlf",
			raw:  "x-litellm-api-key: Bearer " + planted + "\r\n",
			want: ExtraHeadersEnv + ": CR or LF inside a header value",
		},
		{
			name: "cr in value",
			raw:  "x-litellm-api-key: Bearer " + planted + "\rrest",
			want: ExtraHeadersEnv + ": CR or LF inside a header value",
		},
		{
			name: "no colon",
			raw:  "not a header " + planted,
			want: ExtraHeadersEnv + ": line 1 is malformed",
		},
		{
			name: "blank line",
			raw:  "X-A: alpha-token\n\nX-B: beta-token",
			want: ExtraHeadersEnv + ": line 2 is malformed",
		},
		{
			name: "empty value",
			raw:  "X-A:",
			want: ExtraHeadersEnv + ": line 1 is malformed",
		},
		{
			name: "bad name",
			raw:  "Bad Name: " + planted,
			want: ExtraHeadersEnv + ": line 1 is malformed",
		},
		{
			name: "tab in value",
			raw:  "X-A: alpha\t" + planted,
			want: ExtraHeadersEnv + ": line 1 is malformed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseExtraHeaders(tc.raw)
			if err == nil {
				t.Fatal("accepted malformed input")
			}
			if got != nil {
				t.Fatal("headers returned with error")
			}
			if err.Error() != tc.want {
				t.Fatalf("error %q", err.Error())
			}
			if strings.Contains(err.Error(), planted) {
				t.Fatal("error contains header value")
			}
		})
	}
}

func TestExtraHeaderRedaction(t *testing.T) {
	headers, err := ParseExtraHeaders("x-litellm-api-key: Bearer " + planted)
	if err != nil {
		t.Fatal(err)
	}
	h := headers[0]
	samples := []any{h, &h, headers, &headers}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q"}
	for _, sample := range samples {
		for _, verb := range verbs {
			text := fmt.Sprintf(verb, sample)
			if strings.Contains(text, planted) {
				t.Fatalf("%s leaked header value", verb)
			}
		}
	}
	var hp *Header
	if strings.Contains(fmt.Sprintf("%v %+v %#v", hp, hp, hp), planted) {
		t.Fatal("nil pointer leaked")
	}
	body, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	bodyPtr, err := json.Marshal(&h)
	if err != nil {
		t.Fatal(err)
	}
	secretBody, err := json.Marshal(h.value)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{body, bodyPtr, secretBody} {
		if strings.Contains(string(raw), planted) {
			t.Fatal("json leaked header value")
		}
	}
	if !bytes.Contains(body, []byte("x-litellm-api-key")) {
		t.Fatal("json omitted header name")
	}

	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	logger.Info("headers", "h", h, "hp", &h, "secret", h.value, "list", headers)
	if strings.Contains(log.String(), planted) {
		t.Fatal("slog leaked header value")
	}
	if !strings.Contains(log.String(), "x-litellm-api-key") {
		t.Fatal("slog omitted header name")
	}

	msg := Redact("proxy said Bearer "+planted+" and "+planted, headers)
	if strings.Contains(msg, planted) || !strings.Contains(msg, redacted) {
		t.Fatal("redact left the header value in place")
	}
	vals := Values(headers)
	if len(vals) != 2 || vals[0] != "Bearer "+planted || vals[1] != planted {
		t.Fatal("values did not include the bearer token")
	}
}

func TestApplyOverridesDefaultHeaders(t *testing.T) {
	extra, err := ParseExtraHeaders("authorization: Bearer oauth-planted-token\nContent-Type: text/plain\nx-litellm-api-key: Bearer " + planted)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	Apply(h, "sk-api-key-value", extra)
	if h.Get("Authorization") != "Bearer oauth-planted-token" {
		t.Fatal("extra Authorization did not replace the api key")
	}
	if h.Get("Content-Type") != "text/plain" {
		t.Fatal("extra Content-Type did not replace the default")
	}
	if h.Get("X-Litellm-Api-Key") != "Bearer "+planted {
		t.Fatal("custom header was not set")
	}
	if len(h.Values("Authorization")) != 1 || len(h.Values("X-Litellm-Api-Key")) != 1 {
		t.Fatal("duplicate header values")
	}
}

func TestApplyOmitsEmptyAuthorization(t *testing.T) {
	extra, err := ParseExtraHeaders("x-litellm-api-key: Bearer " + planted)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	Apply(h, "", extra)
	if h.Get("Authorization") != "" {
		t.Fatal("empty api key sent Authorization")
	}
	if strings.Contains(h.Get("Authorization"), "Bearer") {
		t.Fatal("empty bearer")
	}
	if h.Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
	if h.Get("X-Litellm-Api-Key") != "Bearer "+planted {
		t.Fatal("custom header missing")
	}
}

func TestApplyUnsetUnchanged(t *testing.T) {
	withKey := http.Header{}
	Apply(withKey, "sk-api-key-value", nil)
	if len(withKey) != 2 {
		t.Fatalf("unset header count %d", len(withKey))
	}
	if withKey.Get("Authorization") != "Bearer sk-api-key-value" || withKey.Get("Content-Type") != "application/json" {
		t.Fatal("unset request changed default headers")
	}
	empty := http.Header{}
	Apply(empty, "sk-api-key-value", []Header{})
	if len(empty) != len(withKey) || empty.Get("Authorization") != withKey.Get("Authorization") {
		t.Fatal("empty extra headers differ from unset")
	}

	noKey := http.Header{}
	Apply(noKey, "", nil)
	if len(noKey) != 1 || noKey.Get("Authorization") != "" || noKey.Get("Content-Type") != "application/json" {
		t.Fatal("unset empty key changed")
	}
}
