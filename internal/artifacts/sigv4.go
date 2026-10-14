package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// signRequest applies an AWS Signature Version 4 Authorization header.
// payload is the exact request body (nil for an empty body). Only headers
// already on req, plus host and x-amz-date, are signed — callers that need
// x-amz-content-sha256 must set it before signing.
func signRequest(req *http.Request, accessKey, secretKey, region, service string, now time.Time, payload []byte) error {
	if accessKey == "" || secretKey == "" || region == "" || service == "" {
		return errors.New("s3: incomplete signing parameters")
	}
	amzDate := now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)

	sum := sha256.Sum256(payload)
	canon, signedHeaders := canonicalRequest(req, hex.EncodeToString(sum[:]))
	scope := amzDate[:8] + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canon))
	sig := hex.EncodeToString(hmacSHA256(signingKey(secretKey, amzDate[:8], region, service), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, sig,
	))
	return nil
}

func canonicalRequest(req *http.Request, payloadHash string) (string, string) {
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	type header struct {
		name  string
		value string
	}
	headers := []header{{name: "host", value: canonicalHeaderValue(host)}}
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if lower == "host" || lower == "authorization" {
			continue
		}
		cleaned := make([]string, len(values))
		for i, value := range values {
			cleaned[i] = canonicalHeaderValue(value)
		}
		headers = append(headers, header{name: lower, value: strings.Join(cleaned, ",")})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].name < headers[j].name })

	var canonHeaders strings.Builder
	names := make([]string, len(headers))
	for i, h := range headers {
		canonHeaders.WriteString(h.name)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(h.value)
		canonHeaders.WriteByte('\n')
		names[i] = h.name
	}
	signed := strings.Join(names, ";")

	uri := "/"
	query := ""
	if req.URL != nil {
		if escaped := req.URL.EscapedPath(); escaped != "" {
			uri = escaped
		}
		query = canonicalQuery(req.URL)
	}
	canon := strings.Join([]string{
		req.Method,
		uri,
		query,
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	return canon, signed
}

func canonicalHeaderValue(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

func canonicalQuery(u *url.URL) string {
	if u == nil || u.RawQuery == "" {
		return ""
	}
	values := u.Query()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		encodedKey := uriEncode(key)
		vals := append([]string(nil), values[key]...)
		sort.Strings(vals)
		for _, value := range vals {
			parts = append(parts, encodedKey+"="+uriEncode(value))
		}
	}
	return strings.Join(parts, "&")
}

func uriEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&0x0f])
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

const hexUpper = "0123456789ABCDEF"

func encodeKey(key string) string {
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = uriEncode(part)
	}
	return strings.Join(parts, "/")
}

func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
