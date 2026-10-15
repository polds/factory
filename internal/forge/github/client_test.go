package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken = "ghs_SUPER_SECRET_TOKEN_VALUE"
	testAppID = int64(4242)
	testInst  = int64(7)
)

func TestJWTUsesRS256AndClockSkewWindow(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)

	id, err := c.InstallationByRepo(context.Background(), "polds/factory")
	if err != nil {
		t.Fatal(err)
	}
	if id != testInst {
		t.Fatalf("installation %d", id)
	}
	jwt := bearer(t, fake.snapshot()[0].auth)
	header, payload := verifyJWT(t, jwt, &key.PublicKey)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header %#v", header)
	}
	if payload["iss"] != strconv.FormatInt(testAppID, 10) {
		t.Fatalf("iss %#v", payload["iss"])
	}
	iat, exp := numClaim(t, payload, "iat"), numClaim(t, payload, "exp")
	if iat != now.Add(-60*time.Second).Unix() {
		t.Fatalf("iat %d", iat)
	}
	if exp != now.Add(10*time.Minute).Unix() {
		t.Fatalf("exp %d", exp)
	}
	if exp-now.Unix() > int64((10 * time.Minute).Seconds()) {
		t.Fatalf("exp is more than 10 minutes ahead")
	}
}

func TestLoadPrivateKeyAndEnv(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	for _, raw := range [][]byte{pkcs1, pkcs8} {
		path := filepath.Join(t.TempDir(), "app.pem")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadPrivateKey(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.N.Cmp(key.N) != 0 {
			t.Fatal("loaded key does not match")
		}
	}

	t.Setenv("FACTORY_GITHUB_APP_ID", "")
	t.Setenv("FACTORY_GITHUB_APP_PRIVATE_KEY_FILE", "")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "FACTORY_GITHUB_APP_ID") {
		t.Fatalf("missing app id: %v", err)
	}
	t.Setenv("FACTORY_GITHUB_APP_ID", strconv.FormatInt(testAppID, 10))
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "FACTORY_GITHUB_APP_PRIVATE_KEY_FILE") {
		t.Fatalf("missing key file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, pkcs1, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_GITHUB_APP_PRIVATE_KEY_FILE", path)
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppID != testAppID || cfg.Key == nil || cfg.Key.N.Cmp(key.N) != 0 {
		t.Fatalf("config app id %d", cfg.AppID)
	}
}

func TestInstallationTokenScopedBodyCacheAndRefresh(t *testing.T) {
	key := testKey(t)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	now := start
	fake := &fakeGH{installationID: testInst, token: testToken, expires: start.Add(time.Hour)}
	var log bytes.Buffer
	c := newTestClient(t, key, fake, now)
	c.log = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.now = func() time.Time { return now }

	ctx := context.Background()
	tok, err := c.InstallationToken(ctx, testInst, []string{"polds/factory", "other"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value() != testToken {
		t.Fatalf("token value %q", tok.Value())
	}
	if !tok.ExpiresAt().Equal(start.Add(time.Hour)) {
		t.Fatalf("expires %s", tok.ExpiresAt())
	}
	body := tokenBody(t, fake)
	if strings.Join(body.Repositories, ",") != "factory,other" {
		t.Fatalf("repositories %#v", body.Repositories)
	}
	wantPerms := DefaultPermissions()
	if len(body.Permissions) != len(wantPerms) {
		t.Fatalf("permissions %#v", body.Permissions)
	}
	for k, v := range wantPerms {
		if body.Permissions[k] != v {
			t.Fatalf("permission %s=%s want %s", k, body.Permissions[k], v)
		}
	}
	if fake.mints != 1 {
		t.Fatalf("mints %d", fake.mints)
	}

	now = start.Add(54 * time.Minute)
	if _, err := c.InstallationToken(ctx, testInst, []string{"other", "polds/factory"}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if fake.mints != 1 {
		t.Fatalf("cache miss, mints %d", fake.mints)
	}

	now = start.Add(55 * time.Minute)
	fake.token = testToken + "_2"
	fake.expires = now.Add(time.Hour)
	tok, err = c.InstallationToken(ctx, testInst, []string{"polds/factory", "other"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.mints != 2 {
		t.Fatalf("refresh mints %d", fake.mints)
	}
	if tok.Value() != testToken+"_2" {
		t.Fatalf("refreshed token %q", tok.Value())
	}

	if _, err := c.InstallationToken(ctx, testInst, []string{"polds/factory"}, nil); err != nil {
		t.Fatal(err)
	}
	if fake.mints != 3 {
		t.Fatalf("repo scope mints %d", fake.mints)
	}
	narrow := map[string]string{"metadata": "read", "contents": "read"}
	if _, err := c.InstallationToken(ctx, testInst, []string{"polds/factory"}, narrow); err != nil {
		t.Fatal(err)
	}
	got := tokenBody(t, fake)
	if len(got.Permissions) != 2 || got.Permissions["metadata"] != "read" || got.Permissions["contents"] != "read" {
		t.Fatalf("narrow permissions %#v", got.Permissions)
	}
	if strings.Contains(got.Permissions["checks"], "write") {
		t.Fatal("narrow token kept default checks permission")
	}

	if strings.Contains(log.String(), testToken) || strings.Contains(log.String(), testToken+"_2") {
		t.Fatalf("token leaked into logs: %s", log.String())
	}
	for _, req := range fake.snapshot() {
		if strings.Contains(log.String(), bearer(t, req.auth)) {
			t.Fatal("jwt leaked into logs")
		}
	}
}

func TestInstallationTokenClampsExpiryToOneHour(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(2 * time.Hour)}
	c := newTestClient(t, key, fake, now)
	tok, err := c.InstallationToken(context.Background(), testInst, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tok.ExpiresAt().Equal(now.Add(time.Hour)) {
		t.Fatalf("expires %s", tok.ExpiresAt())
	}
}

func TestTokenAbsentFromFormatLogAndJSON(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := newTestClient(t, key, fake, now)
	c.log = log
	ctx := context.Background()
	tok, err := c.InstallationToken(ctx, testInst, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCheckRun(ctx, tok, "polds/factory", "abc123", CheckRun{
		Status:     CheckQueued,
		Summary:    "waiting",
		DetailsURL: "https://factory.example/runs/run_1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 4, "<!-- factory:wi_9 -->", "hello"); err != nil {
		t.Fatal(err)
	}
	log.Info("held", "token", tok)
	rendered := fmt.Sprintf("%s %v %+v %#v %q", tok, tok, tok, tok, tok)
	log.Info("rendered", "text", rendered)
	if strings.Contains(rendered, testToken) || strings.Contains(buf.String(), testToken) {
		t.Fatalf("token visible rendered=%q log=%s", rendered, buf.String())
	}
	raw, err := json.Marshal(tok)
	if err == nil || strings.Contains(string(raw), testToken) {
		t.Fatalf("json %s err=%v", raw, err)
	}
	text, err := tok.MarshalText()
	if err == nil || strings.Contains(string(text), testToken) {
		t.Fatalf("text %s err=%v", text, err)
	}
}

func TestCheckRunLifecycle(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	tok, err := c.InstallationToken(ctx, testInst, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cr := CheckRun{
		Status:     CheckQueued,
		Summary:    "waiting",
		DetailsURL: "https://factory.example/runs/run_1",
	}
	id, err := c.CreateCheckRun(ctx, tok, "polds/factory", "abc123", cr)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("missing check run id")
	}
	cr.Status = CheckInProgress
	cr.Summary = "building"
	startedAt := now.UTC().Format(time.RFC3339)
	if err := c.UpdateCheckRun(ctx, tok, "polds/factory", id, cr); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	completedAt := now.UTC().Format(time.RFC3339)
	cr.Status = CheckCompleted
	cr.Conclusion = "success"
	cr.Summary = "done"
	if err := c.UpdateCheckRun(ctx, tok, "polds/factory", id, cr); err != nil {
		t.Fatal(err)
	}

	var sawQueued, sawProgress, sawDone bool
	for _, req := range fake.snapshot() {
		if !strings.Contains(req.path, "/check-runs") {
			continue
		}
		if req.auth != "Bearer "+testToken {
			t.Fatalf("check auth %q", req.auth)
		}
		var body map[string]any
		if err := json.Unmarshal(req.body, &body); err != nil {
			t.Fatal(err)
		}
		switch req.method + " " + body["status"].(string) {
		case "POST queued":
			sawQueued = true
			if body["head_sha"] != "abc123" || body["name"] != "factory" {
				t.Fatalf("create body %#v", body)
			}
			assertDetails(t, body, "waiting")
		case "PATCH in_progress":
			sawProgress = true
			if _, ok := body["conclusion"]; ok {
				t.Fatalf("in_progress has conclusion %#v", body)
			}
			if body["started_at"] != startedAt {
				t.Fatalf("started_at %#v", body["started_at"])
			}
			if _, ok := body["completed_at"]; ok {
				t.Fatal("in_progress has completed_at")
			}
			assertDetails(t, body, "building")
		case "PATCH completed":
			sawDone = true
			if body["conclusion"] != "success" {
				t.Fatalf("conclusion %#v", body["conclusion"])
			}
			if body["started_at"] != startedAt || body["completed_at"] != completedAt {
				t.Fatalf("times started=%#v completed=%#v", body["started_at"], body["completed_at"])
			}
			assertDetails(t, body, "done")
		default:
			t.Fatalf("unexpected check request %s %#v", req.method, body)
		}
	}
	if !sawQueued || !sawProgress || !sawDone {
		t.Fatalf("lifecycle queued=%v progress=%v done=%v", sawQueued, sawProgress, sawDone)
	}

	if _, err := c.CreateCheckRun(ctx, tok, "polds/factory", "abc123", CheckRun{Status: CheckCompleted}); err == nil {
		t.Fatal("completed check without conclusion succeeded")
	}
}

func TestUpsertIssueCommentIdempotent(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	for i := 0; i < 100; i++ {
		fake.comments = append(fake.comments, ghComment{ID: int64(i + 1), Body: "noise"})
	}
	fake.comments = append(fake.comments, ghComment{ID: 1000, Body: "<!-- factory:wi_01 -->\nold"})
	fake.nextComment = 1000
	c := newTestClient(t, key, fake, now)
	ctx := context.Background()
	tok, err := c.InstallationToken(ctx, testInst, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := "<!-- factory:wi_01 -->"
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 12, marker, "building"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 12, marker, "building"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 12, marker, "done"); err != nil {
		t.Fatal(err)
	}
	posts, patches := 0, 0
	for _, req := range fake.snapshot() {
		if strings.Contains(req.path, "/comments") && req.method == http.MethodPost {
			posts++
		}
		if strings.Contains(req.path, "/issues/comments/") && req.method == http.MethodPatch {
			patches++
			if !strings.HasSuffix(req.path, "/issues/comments/1000") {
				t.Fatalf("patched %s", req.path)
			}
		}
	}
	if posts != 0 || patches != 2 {
		t.Fatalf("posts %d patches %d", posts, patches)
	}
	if len(fake.comments) != 101 {
		t.Fatalf("comment count %d", len(fake.comments))
	}
	if !strings.Contains(fake.comments[100].Body, marker) || !strings.Contains(fake.comments[100].Body, "done") {
		t.Fatalf("body %q", fake.comments[100].Body)
	}
	if strings.Contains(fake.comments[100].Body, "building") {
		t.Fatalf("stale body %q", fake.comments[100].Body)
	}
}

func TestUpsertIssueCommentCreatesOnce(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)
	ctx := context.Background()
	tok, err := c.InstallationToken(ctx, testInst, []string{"factory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := "<!-- factory:wi_02 -->"
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 3, marker, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertIssueComment(ctx, tok, "polds/factory", 3, marker, "hello"); err != nil {
		t.Fatal(err)
	}
	posts := 0
	for _, req := range fake.snapshot() {
		if req.method == http.MethodPost && strings.HasSuffix(req.path, "/issues/3/comments") {
			posts++
			var body struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(req.body, &body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Body, marker) || !strings.Contains(body.Body, "hello") {
				t.Fatalf("create body %q", body.Body)
			}
		}
	}
	if posts != 1 || len(fake.comments) != 1 {
		t.Fatalf("posts %d comments %d", posts, len(fake.comments))
	}
}

func TestPermissionDeniedNamesMissingPermission(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		code int
		call func(*Client) error
		perm string
	}{
		{
			name: "installation 404",
			code: http.StatusNotFound,
			perm: "metadata",
			call: func(c *Client) error {
				_, err := c.InstallationByRepo(context.Background(), "polds/factory")
				return err
			},
		},
		{
			name: "token 403",
			code: http.StatusForbidden,
			perm: "contents:write",
			call: func(c *Client) error {
				_, err := c.InstallationToken(context.Background(), testInst, []string{"factory"}, nil)
				return err
			},
		},
		{
			name: "check 404",
			code: http.StatusNotFound,
			perm: "checks",
			call: func(c *Client) error {
				_, err := c.CreateCheckRun(context.Background(), Token{}, "polds/factory", "abc", CheckRun{Status: CheckQueued})
				return err
			},
		},
		{
			name: "comment 403",
			code: http.StatusForbidden,
			perm: "issues",
			call: func(c *Client) error {
				return c.UpsertIssueComment(context.Background(), Token{}, "polds/factory", 1, "<!-- factory:wi_1 -->", "x")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeGH{
				installationID: testInst,
				token:          testToken,
				expires:        now.Add(time.Hour),
				status:         tc.code,
			}
			c := newTestClient(t, key, fake, now)
			err := tc.call(c)
			if !errors.Is(err, ErrPermission) {
				t.Fatalf("err %v", err)
			}
			var pe *PermissionError
			if !errors.As(err, &pe) {
				t.Fatalf("type %T", err)
			}
			if pe.StatusCode != tc.code || !strings.Contains(pe.Error(), tc.perm) {
				t.Fatalf("permission error %v", err)
			}
		})
	}
}

func TestConcurrentTokenMint(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{installationID: testInst, token: testToken, expires: now.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := c.InstallationToken(context.Background(), testInst, []string{"polds/factory"}, nil)
			if err != nil {
				errCh <- err
				return
			}
			if tok.Value() != testToken {
				errCh <- errors.New("unexpected token")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestEvictsStaleInstallationTokens(t *testing.T) {
	key := testKey(t)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	now := start
	fake := &fakeGH{installationID: testInst, token: testToken, expires: start.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.InstallationToken(ctx, testInst, []string{"alpha"}, nil); err != nil {
		t.Fatal(err)
	}
	now = start.Add(30 * time.Minute)
	fake.mu.Lock()
	fake.expires = now.Add(time.Hour)
	fake.token = testToken + "_b"
	fake.mu.Unlock()
	if _, err := c.InstallationToken(ctx, testInst, []string{"beta"}, nil); err != nil {
		t.Fatal(err)
	}
	if n := cacheLen(c); n != 2 {
		t.Fatalf("cache %d", n)
	}

	now = start.Add(56 * time.Minute)
	if _, err := c.InstallationToken(ctx, testInst, []string{"beta"}, nil); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	n := len(c.cache)
	var kept string
	for _, tok := range c.cache {
		kept = tok.Value()
	}
	c.mu.Unlock()
	if n != 1 || kept != testToken+"_b" {
		t.Fatalf("after eviction cache %d kept %q", n, kept)
	}
	fake.mu.Lock()
	mints := fake.mints
	fake.mu.Unlock()
	if mints != 2 {
		t.Fatalf("mints %d", mints)
	}
}

func cacheLen(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

func TestRejectsUnscopedTokenAndBadRepo(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	fake := &fakeGH{token: testToken, expires: now.Add(time.Hour)}
	c := newTestClient(t, key, fake, now)
	if _, err := c.InstallationToken(context.Background(), testInst, nil, nil); err == nil {
		t.Fatal("empty repos minted a token")
	}
	if fake.mints != 0 {
		t.Fatalf("mints %d", fake.mints)
	}
	if _, err := c.InstallationByRepo(context.Background(), "noslash"); err == nil {
		t.Fatal("accepted repo without owner")
	}
	if _, err := c.InstallationToken(context.Background(), testInst, []string{"acme/api", "other/api"}, nil); err == nil {
		t.Fatal("accepted repositories from two owners")
	}
	if fake.mints != 0 {
		t.Fatalf("mints %d", fake.mints)
	}
}

func assertDetails(t *testing.T, body map[string]any, summary string) {
	t.Helper()
	if body["details_url"] != "https://factory.example/runs/run_1" {
		t.Fatalf("details_url %#v", body["details_url"])
	}
	out, _ := body["output"].(map[string]any)
	if out["summary"] != summary || out["title"] != "factory" {
		t.Fatalf("output %#v", out)
	}
}

func tokenBody(t *testing.T, fake *fakeGH) tokenRequest {
	t.Helper()
	reqs := fake.snapshot()
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.Contains(reqs[i].path, "/access_tokens") {
			var body tokenRequest
			if err := json.Unmarshal(reqs[i].body, &body); err != nil {
				t.Fatal(err)
			}
			return body
		}
	}
	t.Fatal("no token request")
	return tokenRequest{}
}

func bearer(t *testing.T, header string) string {
	t.Helper()
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.TrimPrefix(header, prefix) == "" {
		t.Fatalf("authorization %q", header)
	}
	return strings.TrimPrefix(header, prefix)
}

func verifyJWT(t *testing.T, raw string, pub *rsa.PublicKey) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts %d", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal(err)
	}
	return decodeJSONPart(t, parts[0]), decodeJSONPart(t, parts[1])
}

func decodeJSONPart(t *testing.T, raw string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func numClaim(t *testing.T, payload map[string]any, name string) int64 {
	t.Helper()
	switch v := payload[name].(type) {
	case float64:
		return int64(v)
	default:
		t.Fatalf("%s %#v", name, payload[name])
		return 0
	}
}

func newTestClient(t *testing.T, key *rsa.PrivateKey, fake *fakeGH, now time.Time) *Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := New(Config{
		AppID:   testAppID,
		Key:     key,
		BaseURL: srv.URL,
		Now:     func() time.Time { return now },
		Log:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		sharedKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
	})
	return sharedKey
}

var (
	keyOnce   sync.Once
	sharedKey *rsa.PrivateKey
)

type tokenRequest struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

type ghComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type recorded struct {
	method  string
	path    string
	auth    string
	body    []byte
	accept  string
	version string
}

type fakeGH struct {
	mu             sync.Mutex
	installationID int64
	token          string
	expires        time.Time
	mints          int
	reqs           []recorded
	comments       []ghComment
	nextComment    int64
	nextCheck      int64
	status         int
}

func (f *fakeGH) snapshot() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recorded, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{
		method:  r.Method,
		path:    r.URL.Path,
		auth:    r.Header.Get("Authorization"),
		body:    body,
		accept:  r.Header.Get("Accept"),
		version: r.Header.Get("X-GitHub-Api-Version"),
	})
	status := f.status
	f.mu.Unlock()
	if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") == "" || r.Header.Get("User-Agent") == "" {
		http.Error(w, "missing github headers", http.StatusBadRequest)
		return
	}
	if status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
		f.writeJSON(w, http.StatusOK, map[string]any{"id": f.installationID})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
		f.mu.Lock()
		f.mints++
		token, exp := f.token, f.expires
		f.mu.Unlock()
		f.writeJSON(w, http.StatusCreated, map[string]any{
			"token":      token,
			"expires_at": exp.UTC().Format(time.RFC3339),
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/check-runs"):
		f.mu.Lock()
		f.nextCheck++
		id := f.nextCheck
		f.mu.Unlock()
		f.writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/check-runs/"):
		f.writeJSON(w, http.StatusOK, map[string]any{"id": 1})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
		page := 1
		if raw := r.URL.Query().Get("page"); raw != "" {
			page, err = strconv.Atoi(raw)
			if err != nil {
				http.Error(w, "page", http.StatusBadRequest)
				return
			}
		}
		f.mu.Lock()
		comments := append([]ghComment(nil), f.comments...)
		f.mu.Unlock()
		start := (page - 1) * 100
		if start > len(comments) {
			start = len(comments)
		}
		end := start + 100
		if end > len(comments) {
			end = len(comments)
		}
		f.writeJSON(w, http.StatusOK, comments[start:end])
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
		var got struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextComment++
		id := f.nextComment
		f.comments = append(f.comments, ghComment{ID: id, Body: got.Body})
		f.mu.Unlock()
		f.writeJSON(w, http.StatusCreated, map[string]any{"id": id, "body": got.Body})
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/"):
		var got struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idRaw := strings.TrimPrefix(r.URL.Path, "/repos/")
		parts := strings.Split(idRaw, "/")
		id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil {
			http.Error(w, "id", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.comments {
			if f.comments[i].ID == id {
				f.comments[i].Body = got.Body
				f.writeJSON(w, http.StatusOK, f.comments[i])
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGH) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}
