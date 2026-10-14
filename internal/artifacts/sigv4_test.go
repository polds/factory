package artifacts

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSigV4GetVanilla matches the AWS Signature Version 4 suite case
// get-vanilla (awslabs/aws-c-auth v0.3.15, tests/aws-sig-v4-test-suite).
// Credentials are the public example pair published with that suite.
func TestSigV4GetVanilla(t *testing.T) {
	req := requestFromSigV4(t, "testdata/get-vanilla/get-vanilla.req")
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	canon, _ := canonicalRequest(req, emptyHash)
	if canon != readFixture(t, "testdata/get-vanilla/get-vanilla.creq") {
		t.Fatalf("canonical request\n got: %q\nwant: %q", canon, readFixture(t, "testdata/get-vanilla/get-vanilla.creq"))
	}
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	const (
		accessKey = "AKIDEXAMPLE"
		secretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		region    = "us-east-1"
		service   = "service"
	)
	if err := signRequest(req, accessKey, secretKey, region, service, now, nil); err != nil {
		t.Fatal(err)
	}
	got := req.Header.Get("Authorization")
	want := strings.TrimRight(readFixture(t, "testdata/get-vanilla/get-vanilla.authz"), "\r\n")
	if got != want {
		t.Fatalf("Authorization\n got: %s\nwant: %s", got, want)
	}
}

func requestFromSigV4(t *testing.T, path string) *http.Request {
	t.Helper()
	lines := strings.Split(readFixture(t, path), "\n")
	if lines[0] == "" {
		t.Fatal("empty request")
	}
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 {
		t.Fatalf("request line %q", lines[0])
	}
	method, uri := parts[0], parts[1]
	host := ""
	header := make(http.Header)
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("header %q", line)
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "host") {
			host = value
			continue
		}
		header.Add(name, value)
	}
	if host == "" {
		t.Fatal("missing host")
	}
	req, err := http.NewRequest(method, "https://"+host+uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for k, vs := range header {
		req.Header[k] = vs
	}
	return req
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
