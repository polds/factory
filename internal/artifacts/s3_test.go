package artifacts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	endpoint := os.Getenv("FACTORY_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("FACTORY_TEST_S3_ENDPOINT not set")
	}
	s := &S3{
		Endpoint:  endpoint,
		Bucket:    "factory",
		AccessKey: os.Getenv("FACTORY_TEST_S3_KEY"),
		SecretKey: os.Getenv("FACTORY_TEST_S3_SECRET"),
		Region:    "us-east-1",
		PathStyle: true,
	}
	ctx := context.Background()
	key := "runs/run_test/exec_test/events.ndjson"
	body := []byte("{\"type\":\"message.delta\"}\n{\"type\":\"run.terminal\"}\n")
	if err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/x-ndjson"); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q want %q", got, body)
	}
}

func TestPutRetries5xxThreeTimesWithBackoff(t *testing.T) {
	const payload = "hi\n"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(got) != payload {
			t.Errorf("attempt %d body %q", calls, got)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("attempt %d authorization %q", calls, r.Header.Get("Authorization"))
		}
		if calls < 4 {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var sleeps []time.Duration
	s := testS3(srv.URL, func(d time.Duration) { sleeps = append(sleeps, d) })
	err := s.Put(context.Background(), "runs/r/e/events.ndjson", strings.NewReader(payload), int64(len(payload)), "application/x-ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("attempts = %d, want 4 (initial try plus 3 retries)", calls)
	}
	if len(sleeps) != 3 {
		t.Fatalf("backoffs = %d, want 3", len(sleeps))
	}
	for i := 1; i < len(sleeps); i++ {
		if sleeps[i] <= sleeps[i-1] {
			t.Fatalf("backoff not increasing: %v", sleeps)
		}
	}
}

func TestGetRetries5xxThreeTimes(t *testing.T) {
	const payload = "{\"type\":\"run.terminal\"}\n"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if calls < 4 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	var sleeps []time.Duration
	s := testS3(srv.URL, func(d time.Duration) { sleeps = append(sleeps, d) })
	rc, err := s.Get(context.Background(), "runs/r/e/events.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("got %q", got)
	}
	if calls != 4 {
		t.Fatalf("attempts = %d, want 4", calls)
	}
	if len(sleeps) != 3 {
		t.Fatalf("backoffs = %d, want 3", len(sleeps))
	}
}

func TestPutDoesNotRetryClientError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	var sleeps []time.Duration
	s := testS3(srv.URL, func(d time.Duration) { sleeps = append(sleeps, d) })
	err := s.Put(context.Background(), "runs/r/e/events.ndjson", strings.NewReader("x"), 1, "text/plain")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
	if len(sleeps) != 0 {
		t.Fatalf("backoffs = %v, want none", sleeps)
	}
}

func TestErrorStringsOmitCredentials(t *testing.T) {
	const (
		accessKey = "AKIAPLANTEDACCESSKEY"
		secretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYplanted"
	)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if strings.Contains(r.URL.String(), secretKey) || strings.Contains(auth, secretKey) {
			t.Errorf("secret key on the wire")
		}
		for name, values := range r.Header {
			for _, value := range values {
				if strings.Contains(name, secretKey) || strings.Contains(value, secretKey) {
					t.Errorf("secret key in header %s", name)
				}
			}
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, "denied access=%s secret=%s auth=%s", accessKey, secretKey, auth)
	}))
	defer srv.Close()

	s := testS3(srv.URL, nil)
	s.AccessKey = accessKey
	s.SecretKey = secretKey
	rc, err := s.Get(context.Background(), "runs/run_planted/exec_planted/events.ndjson")
	if rc != nil {
		_ = rc.Close()
	}
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, accessKey) || strings.Contains(msg, secretKey) {
		t.Fatalf("credentials in error string: %s", strings.ReplaceAll(strings.ReplaceAll(msg, accessKey, "[access]"), secretKey, "[secret]"))
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+accessKey+"/") {
		t.Fatalf("authorization %q", auth)
	}
}

func testS3(endpoint string, backoff func(time.Duration)) *S3 {
	return &S3{
		Endpoint:  endpoint,
		Bucket:    "factory",
		AccessKey: "AKIATEST",
		SecretKey: "test-secret-value",
		Region:    "us-east-1",
		PathStyle: true,
		backoff:   backoff,
	}
}
