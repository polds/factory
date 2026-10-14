// Package artifacts archives execution transcripts in an S3-compatible object store.
// Event logs use the key layout runs/<run_id>/<execution_id>/events.ndjson.
package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3 is a SigV4 client for one bucket. HTTP is optional; nil uses http.DefaultClient.
// backoff, when set, replaces the wait between 5xx retries. Production callers leave it nil.
type S3 struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	PathStyle bool
	HTTP      *http.Client

	backoff func(time.Duration)
}

// Put stores body at key. size is the exact byte length of body.
// A 5xx response is retried three times with increasing backoff.
func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	payload, err := readPayload(body, size)
	if err != nil {
		return s.redact(err)
	}
	u, err := s.objectURL(key)
	if err != nil {
		return s.redact(err)
	}
	out, err := s.do(ctx, func() (*http.Request, error) {
		return s.newRequest(ctx, http.MethodPut, u, payload, contentType)
	})
	if err != nil {
		return err
	}
	if out.status/100 != 2 {
		return s.redact(statusError(out.status, out.body))
	}
	return nil
}

// Get returns the object at key. The caller closes the result.
// A 5xx response is retried three times with increasing backoff.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	u, err := s.objectURL(key)
	if err != nil {
		return nil, s.redact(err)
	}
	out, err := s.do(ctx, func() (*http.Request, error) {
		return s.newRequest(ctx, http.MethodGet, u, nil, "")
	})
	if err != nil {
		return nil, err
	}
	if out.status/100 != 2 {
		return nil, s.redact(statusError(out.status, out.body))
	}
	return io.NopCloser(bytes.NewReader(out.body)), nil
}

type responseBody struct {
	status int
	body   []byte
}

func (s *S3) do(ctx context.Context, build func() (*http.Request, error)) (responseBody, error) {
	const retries = 3
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := s.wait(ctx, attempt); err != nil {
				return responseBody{}, s.redact(err)
			}
		}
		req, err := build()
		if err != nil {
			return responseBody{}, s.redact(err)
		}
		resp, err := s.client().Do(req)
		if err != nil {
			return responseBody{}, s.redact(err)
		}
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && attempt < retries {
			snippet, readErr := readLimited(resp.Body)
			if readErr != nil {
				_ = resp.Body.Close()
				return responseBody{}, s.redact(readErr)
			}
			if _, copyErr := io.Copy(io.Discard, resp.Body); copyErr != nil {
				_ = resp.Body.Close()
				return responseBody{}, s.redact(copyErr)
			}
			if closeErr := resp.Body.Close(); closeErr != nil {
				return responseBody{}, s.redact(closeErr)
			}
			last = statusError(resp.StatusCode, snippet)
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return responseBody{}, s.redact(readErr)
		}
		if closeErr != nil {
			return responseBody{}, s.redact(closeErr)
		}
		return responseBody{status: resp.StatusCode, body: data}, nil
	}
	if last == nil {
		last = errors.New("s3: retries exhausted")
	}
	return responseBody{}, s.redact(last)
}

func (s *S3) newRequest(ctx context.Context, method string, u *url.URL, payload []byte, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Amz-Content-Sha256", sha256Hex(payload))
	if err := signRequest(req, s.AccessKey, s.SecretKey, s.Region, "s3", time.Now(), payload); err != nil {
		return nil, err
	}
	return req, nil
}

func (s *S3) objectURL(key string) (*url.URL, error) {
	endpoint := strings.TrimSpace(s.Endpoint)
	if endpoint == "" {
		return nil, errors.New("s3: endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3: parse endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("s3: endpoint must include a host")
	}
	if strings.TrimSpace(s.Bucket) == "" {
		return nil, errors.New("s3: bucket is empty")
	}
	key = strings.TrimPrefix(key, "/")
	if s.PathStyle {
		u.Path = "/" + s.Bucket + "/" + key
		u.RawPath = "/" + uriEncode(s.Bucket) + "/" + encodeKey(key)
	} else {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
		u.RawPath = "/" + encodeKey(key)
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func (s *S3) wait(ctx context.Context, retry int) error {
	d := time.Duration(100<<(retry-1)) * time.Millisecond
	if s.backoff != nil {
		s.backoff(d)
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *S3) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

func (s *S3) redact(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := redactString(msg, s.SecretKey)
	redacted = redactString(redacted, s.AccessKey)
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}

func redactString(msg, secret string) string {
	if secret == "" || !strings.Contains(msg, secret) {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "[REDACTED]")
}

func readPayload(body io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("s3: negative object size")
	}
	if body == nil {
		if size != 0 {
			return nil, errors.New("s3: empty body")
		}
		return []byte{}, nil
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("s3: read body: %w", err)
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("s3: body is %d bytes, size is %d", len(data), size)
	}
	return data, nil
}

func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 2048))
}

func statusError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("s3: status %d", status)
	}
	return fmt.Errorf("s3: status %d: %s", status, msg)
}
