package ingress

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/polds/factory/internal/ids"
)

// newDeliveryID returns dlv_ plus 26 Crockford characters. The layout matches
// store and handoff ids: 48-bit millisecond timestamp, then 80 bits of
// cryptographic randomness. now supplies the timestamp.
func newDeliveryID(now time.Time) (string, error) {
	var entropy [10]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("ingress: id entropy: %w", err)
	}
	return formatDeliveryID(now, entropy[:])
}

func formatDeliveryID(now time.Time, entropy []byte) (string, error) {
	id, err := ids.Format(PrefixDelivery, uint64(now.UnixMilli()), entropy)
	if err != nil {
		return "", fmt.Errorf("ingress: %w", err)
	}
	return id, nil
}
