package webhooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DedupBucket is the window used when a webhook sets no deliveryIdHeader.
const DedupBucket = 5 * time.Minute

var errMissingDeliveryID = errors.New("webhooks: missing delivery id")

// DeliveryID is the dedup key for one verified body.
// An empty headerName hashes the raw bytes into a DedupBucket of now:
// hex(SHA-256(body)) + "/" + floor(unix seconds / 300).
// A set headerName must pass ValidateDeliveryIDHeader; the id is that
// header's trimmed value, and a missing value is an error rather than a
// fallback to the body hash.
func DeliveryID(headerName string, header http.Header, body []byte, now time.Time) (string, error) {
	if err := ValidateDeliveryIDHeader(headerName); err != nil {
		return "", err
	}
	if headerName == "" {
		return bodyDeliveryID(body, now), nil
	}
	if header == nil {
		return "", errMissingDeliveryID
	}
	value := strings.TrimSpace(header.Get(headerName))
	if value == "" {
		return "", errMissingDeliveryID
	}
	return value, nil
}

func bodyDeliveryID(body []byte, now time.Time) string {
	sum := sha256.Sum256(body)
	bucket := now.UTC().Unix() / int64(DedupBucket/time.Second)
	return hex.EncodeToString(sum[:]) + "/" + strconv.FormatInt(bucket, 10)
}
