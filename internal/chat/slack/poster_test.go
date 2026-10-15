package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "xoxb-test-secret"

func TestPostAndUpdate(t *testing.T) {
	var mu sync.Mutex
	var calls []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		calls = append(calls, captured{
			path: r.URL.Path,
			auth: r.Header.Get("Authorization"),
			body: string(body),
		})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1600000000.000100","channel":"C0FACTORY"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(testToken, WithBaseURL(srv.URL+"/"))
	ctx := context.Background()
	blocks := json.RawMessage(`[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`)
	ts, err := c.Post(ctx, "C0FACTORY", "1600000000.000001", "hello", blocks)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1600000000.000100" {
		t.Fatalf("ts %s", ts)
	}
	if err := c.Update(ctx, "C0FACTORY", ts, "hello again", nil); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("calls %d", len(calls))
	}
	for _, call := range calls {
		if call.auth != "Bearer "+testToken {
			t.Fatalf("auth %q", call.auth)
		}
		if strings.Contains(call.body, testToken) {
			t.Fatalf("token in body %s", call.body)
		}
	}
	assertJSONField(t, calls[0].body, "thread_ts", "1600000000.000001")
	assertJSONField(t, calls[0].body, "text", "hello")
	assertJSONField(t, calls[0].body, "channel", "C0FACTORY")
	if !strings.Contains(calls[0].body, `"blocks"`) {
		t.Fatalf("post body missing blocks: %s", calls[0].body)
	}
	if calls[0].path != "/chat.postMessage" {
		t.Fatalf("path %s", calls[0].path)
	}
	assertJSONField(t, calls[1].body, "ts", "1600000000.000100")
	assertJSONField(t, calls[1].body, "text", "hello again")
	if strings.Contains(calls[1].body, "thread_ts") {
		t.Fatalf("update sent thread_ts: %s", calls[1].body)
	}
	if strings.Contains(calls[1].body, "blocks") {
		t.Fatalf("update sent empty blocks: %s", calls[1].body)
	}
	if calls[1].path != "/chat.update" {
		t.Fatalf("path %s", calls[1].path)
	}
}

func TestPostOmitsEmptyThreadAndBlocks(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1.2"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	if _, err := c.Post(context.Background(), "C0FACTORY", "", "hello", json.RawMessage(" null ")); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["thread_ts"]; ok {
		t.Fatalf("thread_ts present: %s", body)
	}
	if _, ok := m["blocks"]; ok {
		t.Fatalf("blocks present: %s", body)
	}
}

func TestAPIErrorOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	_, err := c.Post(context.Background(), "Cmissing", "", "hello", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var api *Error
	if !errors.As(err, &api) {
		t.Fatalf("error type %T %v", err, err)
	}
	if api.Method != "chat.postMessage" || api.Code != "channel_not_found" {
		t.Fatalf("api error %+v", api)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
}

func TestErrorBodyTokenIsScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"ok":false,"error":"%s"}`, testToken)
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	err := c.Update(context.Background(), "C0FACTORY", "1.2", "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "xoxb-") {
		t.Fatalf("token in error %v", err)
	}
}

func TestRateLimitRetriesAfterHeader(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"9.9"}`))
	}))
	t.Cleanup(srv.Close)

	var slept []time.Duration
	c := New(testToken, WithBaseURL(srv.URL), WithSleep(func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}))
	ts, err := c.Post(context.Background(), "C0FACTORY", "", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "9.9" {
		t.Fatalf("ts %s", ts)
	}
	if n.Load() != 2 {
		t.Fatalf("requests %d", n.Load())
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept %v", slept)
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, "slow down %s", testToken)
	}))
	t.Cleanup(srv.Close)
	var sleeps atomic.Int32
	c := New(testToken, WithBaseURL(srv.URL), WithRateLimitRetries(3), WithSleep(func(context.Context, time.Duration) error {
		sleeps.Add(1)
		return nil
	}))
	_, err := c.Post(context.Background(), "C0FACTORY", "", "hello", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
	if n.Load() != 4 || sleeps.Load() != 3 {
		t.Fatalf("requests %d sleeps %d", n.Load(), sleeps.Load())
	}
}

func TestRateLimitMissingAndInvalidRetryAfter(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{header: "", want: time.Second},
		{header: "abc", want: time.Second},
		{header: "0", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if n.Add(1) == 1 {
					if tc.header != "" {
						w.Header().Set("Retry-After", tc.header)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"ts":"1"}`))
			}))
			t.Cleanup(srv.Close)
			var got time.Duration
			c := New(testToken, WithBaseURL(srv.URL), WithSleep(func(_ context.Context, d time.Duration) error {
				got = d
				return nil
			}))
			if _, err := c.Post(context.Background(), "C", "", "t", nil); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("wait %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCancelledWhileWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	c := New(testToken, WithBaseURL(srv.URL), WithSleep(func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}))
	_, err := c.Post(ctx, "C", "", "t", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
}

func TestInvalidBlocks(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	_, err := c.Post(context.Background(), "C", "", "t", json.RawMessage(`{`))
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("server was called")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
}

func TestMissingTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	_, err := c.Post(context.Background(), "C", "", "t", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
}

func TestDoesNotFollowRedirects(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path == "/chat.postMessage" {
			w.Header().Set("Location", "/stolen")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	_, err := c.Post(context.Background(), "C", "", "t", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if n.Load() != 1 {
		t.Fatalf("requests %d", n.Load())
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token in error %v", err)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	var got string
	c := New(testToken, WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r.URL.String()
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("auth %s", r.Header.Get("Authorization"))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"ts":"1.0"}`)),
				Request:    r,
			}, nil
		}),
	}))
	ts, err := c.Post(context.Background(), "C0FACTORY", "", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1.0" || got != "https://slack.com/api/chat.postMessage" {
		t.Fatalf("ts %s url %s", ts, got)
	}
}

func TestClientStringRedactsToken(t *testing.T) {
	c := New(testToken, WithBaseURL("https://slack.example/api"))
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		s := fmt.Sprintf(format, c)
		if strings.Contains(s, testToken) || strings.Contains(s, "xoxb-") {
			t.Fatalf("%s leaked token: %s", format, s)
		}
	}
}

func TestConcurrentPosts(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = w.Write([]byte(`{"ok":true,"ts":"3.3"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(testToken, WithBaseURL(srv.URL))
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts, err := c.Post(context.Background(), "C0FACTORY", "", "hi", nil)
			if err != nil {
				errCh <- err
				return
			}
			if ts != "3.3" {
				errCh <- errors.New("bad ts " + ts)
			}
			if err := c.Update(context.Background(), "C0FACTORY", ts, "edited", nil); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if n.Load() != 32 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestUpdateRateLimit(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.update" {
			http.NotFound(w, r)
			return
		}
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1"}`))
	}))
	t.Cleanup(srv.Close)
	var slept []time.Duration
	c := New(testToken, WithBaseURL(srv.URL), WithSleep(func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}))
	if err := c.Update(context.Background(), "C", "1.2", "text", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || len(slept) != 1 || slept[0] != 5*time.Second {
		t.Fatalf("requests %d slept %v", n.Load(), slept)
	}
}

type captured struct {
	path string
	auth string
	body string
}

func assertJSONField(t *testing.T, body, key, want string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	got, _ := m[key].(string)
	if got != want {
		t.Fatalf("%s = %q, want %q (%s)", key, got, want, body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
