package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestTransientClassifiesTokenFetchErrors(t *testing.T) {
	timeout := &net.DNSError{Err: "i/o timeout", Name: "api.github.com", IsTimeout: true}
	reset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	canceled := &urlError{err: context.Canceled}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "timeout", err: timeout, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "reset", err: reset, want: true},
		{name: "refused", err: refused, want: true},
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "api.github.com", IsNotFound: true}, want: true},
		{name: "http 500", err: &StatusError{Method: "POST", Path: "/access_tokens", StatusCode: http.StatusInternalServerError}, want: true},
		{name: "http 502", err: &StatusError{Method: "POST", Path: "/access_tokens", StatusCode: http.StatusBadGateway}, want: true},
		{name: "wrapped 503", err: fmt.Errorf("server: github token: %w", &StatusError{StatusCode: http.StatusServiceUnavailable}), want: true},
		{name: "http 401", err: &StatusError{Method: "POST", Path: "/access_tokens", StatusCode: http.StatusUnauthorized, Detail: "Bad credentials"}, want: false},
		{name: "http 403", err: &PermissionError{Op: "access_token", StatusCode: http.StatusForbidden, Permission: "contents:write"}, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "canceled wrap", err: canceled, want: false},
		{name: "bad key", err: errors.New("github: sign jwt: crypto/rsa: verification error"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Transient(tc.err); got != tc.want {
				t.Fatalf("Transient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// urlError is a net.Error that wraps context.Canceled. net/url.Error does
// the same for a canceled request, and that must stay non-transient.
type urlError struct{ err error }

func (e *urlError) Error() string   { return "Get \"https://api.github.com\": " + e.err.Error() }
func (e *urlError) Timeout() bool   { return false }
func (e *urlError) Temporary() bool { return false }
func (e *urlError) Unwrap() error   { return e.err }

func TestInstallationTokenHTTPStatus(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		code int
		want bool
	}{
		{code: http.StatusBadGateway, want: true},
		{code: http.StatusUnauthorized, want: false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			fake := &fakeGH{
				installationID: testInst,
				token:          testToken,
				expires:        now.Add(time.Hour),
				status:         tc.code,
			}
			c := newTestClient(t, key, fake, now)
			_, err := c.InstallationToken(context.Background(), testInst, []string{"factory"}, nil)
			var status *StatusError
			if !errors.As(err, &status) || status.StatusCode != tc.code {
				t.Fatalf("status %v", err)
			}
			if Transient(err) != tc.want || Transient(fmt.Errorf("server: github token: %w", err)) != tc.want {
				t.Fatalf("transient %v err %v", Transient(err), err)
			}
		})
	}
}

func TestInstallationLookupConnectionRefused(t *testing.T) {
	c, err := New(Config{
		AppID:   1,
		Key:     testKey(t),
		BaseURL: "http://127.0.0.1:1",
		HTTP:    &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.InstallationByRepo(context.Background(), "acme/payments")
	if !Transient(err) {
		t.Fatalf("refused connection was not transient: %v", err)
	}
}
