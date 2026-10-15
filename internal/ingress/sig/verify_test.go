package sig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	githubSecret = "It's a Secret to Everybody"
	githubBody   = "Hello, World!"
	githubSig    = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	slackSecret = "8f742231b10e8888abcd99yyyzzz85a5"
	slackBody   = "token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c"
	slackTS     = "1531420618"
	slackSig    = "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503"

	stdSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	stdID     = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	stdTS     = "1614265330"
	stdBody   = `{"test": 2432232314}`
	stdSig    = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="

	stripeSecret = "whsec_test_secret"
	stripeBody   = `{"id":"evt_test_webhook","object":"event"}`
	stripeTS     = "1492774577"
	stripeSig    = "88a022085c6bdb887b02cb26ff76dd681234d9675c0f22844059f55552a8883a"

	sentrySecret = "sentry-client-secret"
	sentryBody   = `{"action":"created"}`
	sentrySig    = "9b05c6b98d963e915adee9a9a31112772be0463d294ca2a810c12adc468d8822"

	vercelSecret = "vercel-secret"
	vercelBody   = `{"type":"project.created"}`
	vercelSig    = "8595cabbbe514d7dd2ba6e719f230b60be094e1d"

	linearSecret = "linear-secret"
	linearBody   = `{"webhookTimestamp":1700000000000,"action":"create"}`
	linearSig    = "1ca937d0e82f9484547aab98b83755a7765f59f69f8b8b8d2a49264b5968015e"

	pagerDutySecret = "lDQHScfUeXUKaQRNF+8XIiDKZ7XX3itBAYzwU0TARw8lJqRnkKl2iB1anSb0Z+IK"
	pagerDutySig    = "v1=0c0b9495b893a39e70d1fea2fe11fbe0a825f88b9f67846f6cc07dd2bc5476cd"

	gitlabSecret = "gitlab-secret-token"
)

func TestSchemeTable(t *testing.T) {
	t.Parallel()
	pagerBody := readPagerDuty(t)
	// Schemes without a signed timestamp keep a stale-clock row so every
	// scheme has the same five cases. A distant now must not reject them.
	cases := []schemeCase{
		githubCase("valid", githubSig, githubBody, time.Unix(0, 0), nil),
		githubCase("bad_sig", flipHexPrefix(githubSig), githubBody, time.Unix(0, 0), ErrSignature),
		githubCase("stale_clock_ignored", githubSig, githubBody, time.Unix(0, 0).Add(24*time.Hour), nil),
		githubCase("tampered_body", githubSig, githubBody+"?", time.Unix(0, 0), ErrSignature),
		githubCase("missing_header", "", githubBody, time.Unix(0, 0), ErrMissing),

		gitlabCase("valid", gitlabSecret, `{"object_kind":"push"}`, time.Unix(0, 0), nil),
		gitlabCase("bad_sig", "gitlab-secret-tokem", `{"object_kind":"push"}`, time.Unix(0, 0), ErrSignature),
		gitlabCase("stale_clock_ignored", gitlabSecret, `{"object_kind":"push"}`, time.Unix(0, 0).Add(24*time.Hour), nil),
		gitlabCase("tampered_body_not_signed", gitlabSecret, `{"object_kind":"tag"}`, time.Unix(0, 0), nil),
		gitlabCase("missing_header", "", `{"object_kind":"push"}`, time.Unix(0, 0), ErrMissing),

		stripeCase("valid", "t="+stripeTS+",v1="+stripeSig, stripeBody, unix(stripeTS), nil),
		stripeCase("bad_sig", "t="+stripeTS+",v1="+flipHex(stripeSig), stripeBody, unix(stripeTS), ErrSignature),
		stripeCase("stale", "t="+stripeTS+",v1="+stripeSig, stripeBody, unix(stripeTS).Add(stripeTolerance+time.Second), ErrStale),
		stripeCase("tampered_body", "t="+stripeTS+",v1="+stripeSig, stripeBody+" ", unix(stripeTS), ErrSignature),
		stripeCase("missing_header", "", stripeBody, unix(stripeTS), ErrMissing),

		sentryCase("valid", sentrySig, sentryBody, nil),
		sentryCase("bad_sig", flipHex(sentrySig), sentryBody, ErrSignature),
		sentryCase("stale_clock_ignored", sentrySig, sentryBody, nil),
		sentryCase("tampered_body", sentrySig, sentryBody+" ", ErrSignature),
		sentryCase("missing_header", "", sentryBody, ErrMissing),

		pagerCase("valid", pagerDutySig, pagerBody, nil),
		pagerCase("bad_sig", "v1="+flipHex(trimV1(pagerDutySig)), pagerBody, ErrSignature),
		pagerCase("stale_clock_ignored", pagerDutySig, pagerBody, nil),
		pagerCase("tampered_body", pagerDutySig, append(append([]byte{}, pagerBody...), ' '), ErrSignature),
		pagerCase("missing_header", "", pagerBody, ErrMissing),

		vercelCase("valid", vercelSig, vercelBody, nil),
		vercelCase("bad_sig", flipHex(vercelSig), vercelBody, ErrSignature),
		vercelCase("stale_clock_ignored", vercelSig, vercelBody, nil),
		vercelCase("tampered_body", vercelSig, vercelBody+" ", ErrSignature),
		vercelCase("missing_header", "", vercelBody, ErrMissing),

		stdCase("valid", stdSig, stdBody, unix(stdTS), nil),
		stdCase("bad_sig", "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OA=", stdBody, unix(stdTS), ErrSignature),
		stdCase("stale", stdSig, stdBody, unix(stdTS).Add(standardSkew+time.Second), ErrStale),
		stdCase("tampered_body", stdSig, stdBody+" ", unix(stdTS), ErrSignature),
		stdCase("missing_header", "", stdBody, unix(stdTS), ErrMissing),

		slackCase("valid", slackSig, slackBody, unix(slackTS), nil),
		slackCase("bad_sig", flipHexPrefix(slackSig), slackBody, unix(slackTS), ErrSignature),
		slackCase("stale", slackSig, slackBody, unix(slackTS).Add(slackSkew+time.Second), ErrStale),
		slackCase("tampered_body", slackSig, slackBody+"x", unix(slackTS), ErrSignature),
		slackCase("missing_header", "", slackBody, unix(slackTS), ErrMissing),

		linearCase("valid", linearSig, linearBody, unixMilli(1700000000000), nil),
		linearCase("bad_sig", flipHex(linearSig), linearBody, unixMilli(1700000000000), ErrSignature),
		linearCase("stale", linearSig, linearBody, unixMilli(1700000000000).Add(linearSkew+time.Millisecond), ErrStale),
		linearCase("tampered_body", linearSig, linearBody+" ", unixMilli(1700000000000), ErrSignature),
		linearCase("missing_header", "", linearBody, unixMilli(1700000000000), ErrMissing),
	}
	for _, tc := range cases {
		t.Run(tc.scheme+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := Open(tc.scheme, tc.secrets...)
			if err != nil {
				t.Fatal(err)
			}
			err = v.Verify(tc.header, tc.body, tc.now)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestLinearDoesNotParseBeforeSignature(t *testing.T) {
	t.Parallel()
	v, err := Open(schemeLinear, linearSecret)
	if err != nil {
		t.Fatal(err)
	}
	err = v.Verify(header("Linear-Signature", flipHex(linearSig)), []byte("not-json"), unixMilli(1700000000000))
	if !errors.Is(err, ErrSignature) {
		t.Fatalf("got %v", err)
	}
	body := []byte("not-json")
	err = v.Verify(header("Linear-Signature", hmacHex(linearSecret, body)), body, unixMilli(1700000000000))
	if errors.Is(err, ErrSignature) || !errors.Is(err, ErrMalformed) {
		t.Fatalf("good sig on invalid json: %v", err)
	}
}

func TestStripeMultipleSignaturesAndFutureSkew(t *testing.T) {
	t.Parallel()
	v, err := Open(schemeStripe, stripeSecret)
	if err != nil {
		t.Fatal(err)
	}
	hdr := header("Stripe-Signature", "t="+stripeTS+", v0=deadbeef, v1=00, v1="+stripeSig)
	if err := v.Verify(hdr, []byte(stripeBody), unix(stripeTS)); err != nil {
		t.Fatal(err)
	}
	future := unix(stripeTS).Add(-stripeTolerance - time.Second)
	if err := v.Verify(header("Stripe-Signature", "t="+stripeTS+",v1="+stripeSig), []byte(stripeBody), future); !errors.Is(err, ErrStale) {
		t.Fatalf("future skew: %v", err)
	}
	edge := unix(stripeTS).Add(stripeTolerance)
	if err := v.Verify(header("Stripe-Signature", "t="+stripeTS+",v1="+stripeSig), []byte(stripeBody), edge); err != nil {
		t.Fatalf("boundary: %v", err)
	}
}

func TestPagerDutyMultipleSignatures(t *testing.T) {
	t.Parallel()
	body := readPagerDuty(t)
	v, err := Open(schemePagerDuty, pagerDutySecret, "other-secret")
	if err != nil {
		t.Fatal(err)
	}
	hdr := header("X-PagerDuty-Signature", "v1="+flipHex(trimV1(pagerDutySig))+","+pagerDutySig)
	if err := v.Verify(hdr, body, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	onlyOther := header("X-PagerDuty-Signature", "v2=abcd,"+pagerDutySig)
	if err := v.Verify(onlyOther, body, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(header("X-PagerDuty-Signature", "v2=abcd"), body, time.Unix(0, 0)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("non-v1: %v", err)
	}
}

func TestStandardWebhooksMultipleKeys(t *testing.T) {
	t.Parallel()
	other := "whsec_" + base64.StdEncoding.EncodeToString([]byte("second-secret-key-32bytes!!!"))
	second := signStandard(t, other, stdID, stdTS, []byte(stdBody))
	v, err := Open(schemeStandardWebhooks, stdSecret, other)
	if err != nil {
		t.Fatal(err)
	}
	hdr := headerMap(map[string]string{
		"Webhook-Id":        stdID,
		"Webhook-Timestamp": stdTS,
		"Webhook-Signature": "v1a,aaaa " + second,
	})
	if err := v.Verify(hdr, []byte(stdBody), unix(stdTS)); err != nil {
		t.Fatal(err)
	}
	onlyFirst, err := Open(schemeStandardWebhooks, stdSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := onlyFirst.Verify(hdr, []byte(stdBody), unix(stdTS)); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestGitHubUpperHexAndGitLabPrefix(t *testing.T) {
	t.Parallel()
	v, err := Open(schemeGitHub, githubSecret)
	if err != nil {
		t.Fatal(err)
	}
	upper := "sha256=" + hexUpper(trimPrefix(githubSig, "sha256="))
	if err := v.Verify(header("X-Hub-Signature-256", upper), []byte(githubBody), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(header("X-Hub-Signature-256", trimPrefix(githubSig, "sha256=")), []byte(githubBody), time.Unix(0, 0)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("bare hex: %v", err)
	}
	gl, err := Open(schemeGitLab, gitlabSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := gl.Verify(header("X-Gitlab-Token", gitlabSecret[:len(gitlabSecret)-1]), []byte("x"), time.Unix(0, 0)); !errors.Is(err, ErrSignature) {
		t.Fatalf("prefix token: %v", err)
	}
}

func TestOpenRejectsUnknownAndEmpty(t *testing.T) {
	t.Parallel()
	if _, err := Open("GitHub", "secret"); err == nil {
		t.Fatal("accepted wrong case")
	}
	if _, err := Open(schemeGitHub); err == nil {
		t.Fatal("accepted empty secret list")
	}
	if _, err := Open(schemeGitHub, ""); err == nil {
		t.Fatal("accepted empty secret")
	}
	if _, err := Open(schemeStandardWebhooks, "not base64!!!"); err == nil {
		t.Fatal("accepted non-base64 standard secret")
	}
}

type schemeCase struct {
	name    string
	scheme  string
	secrets []string
	header  http.Header
	body    []byte
	now     time.Time
	want    error
}

func githubCase(name, sig, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("X-Hub-Signature-256", sig)
	}
	return schemeCase{name: name, scheme: schemeGitHub, secrets: []string{githubSecret}, header: h, body: []byte(body), now: now, want: want}
}

func gitlabCase(name, token, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	if token != "" {
		h.Set("X-Gitlab-Token", token)
	}
	return schemeCase{name: name, scheme: schemeGitLab, secrets: []string{gitlabSecret}, header: h, body: []byte(body), now: now, want: want}
}

func stripeCase(name, sig, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("Stripe-Signature", sig)
	}
	return schemeCase{name: name, scheme: schemeStripe, secrets: []string{stripeSecret}, header: h, body: []byte(body), now: now, want: want}
}

func sentryCase(name, sig, body string, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("Sentry-Hook-Signature", sig)
	}
	h.Set("Sentry-Hook-Timestamp", "1")
	return schemeCase{name: name, scheme: schemeSentry, secrets: []string{sentrySecret}, header: h, body: []byte(body), now: time.Unix(10_000_000, 0), want: want}
}

func pagerCase(name, sig string, body []byte, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("X-PagerDuty-Signature", sig)
	}
	return schemeCase{name: name, scheme: schemePagerDuty, secrets: []string{pagerDutySecret}, header: h, body: body, now: time.Unix(0, 0), want: want}
}

func vercelCase(name, sig, body string, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("X-Vercel-Signature", sig)
	}
	return schemeCase{name: name, scheme: schemeVercel, secrets: []string{vercelSecret}, header: h, body: []byte(body), now: time.Unix(0, 0), want: want}
}

func stdCase(name, sig, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	h.Set("Webhook-Id", stdID)
	h.Set("Webhook-Timestamp", stdTS)
	if sig != "" {
		h.Set("Webhook-Signature", sig)
	} else {
		h.Del("Webhook-Signature")
	}
	return schemeCase{name: name, scheme: schemeStandardWebhooks, secrets: []string{stdSecret}, header: h, body: []byte(body), now: now, want: want}
}

func slackCase(name, sig, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", slackTS)
	if sig != "" {
		h.Set("X-Slack-Signature", sig)
	}
	if name == "missing_header" {
		h.Del("X-Slack-Request-Timestamp")
	}
	return schemeCase{name: name, scheme: schemeSlack, secrets: []string{slackSecret}, header: h, body: []byte(body), now: now, want: want}
}

func linearCase(name, sig, body string, now time.Time, want error) schemeCase {
	h := http.Header{}
	if sig != "" {
		h.Set("Linear-Signature", sig)
	}
	return schemeCase{name: name, scheme: schemeLinear, secrets: []string{linearSecret}, header: h, body: []byte(body), now: now, want: want}
}

func header(name, value string) http.Header {
	h := http.Header{}
	h.Set(name, value)
	return h
}

func headerMap(vals map[string]string) http.Header {
	h := http.Header{}
	for k, v := range vals {
		h.Set(k, v)
	}
	return h
}

func unix(sec string) time.Time {
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		panic(err)
	}
	return time.Unix(n, 0)
}

func unixMilli(ms int64) time.Time {
	return time.UnixMilli(ms)
}

func flipHexPrefix(sig string) string {
	i := len(sig) - 1
	return sig[:i] + flipNibble(sig[i])
}

func flipHex(sig string) string {
	return flipHexPrefix(sig)
}

func flipNibble(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}

func trimV1(sig string) string {
	return trimPrefix(sig, "v1=")
}

func trimPrefix(s, prefix string) string {
	if len(s) < len(prefix) || s[:len(prefix)] != prefix {
		return s
	}
	return s[len(prefix):]
}

func hexUpper(s string) string {
	return strings.ToUpper(s)
}

func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func signStandard(t *testing.T, secret, id, ts string, body []byte) string {
	t.Helper()
	key, err := decodeWHSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write([]byte(id + "." + ts + ".")); err != nil {
		t.Fatal(err)
	}
	if _, err := mac.Write(body); err != nil {
		t.Fatal(err)
	}
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func readPagerDuty(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/pagerduty.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
