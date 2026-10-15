package ingress

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

// crockfordAlphabet is Crockford base32 (no I, L, O, or U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

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
	ms := uint64(now.UnixMilli())
	if ms >= 1<<48 {
		return "", fmt.Errorf("ingress: timestamp does not fit in 48 bits")
	}
	if len(entropy) != 10 {
		return "", fmt.Errorf("ingress: entropy must be 10 bytes")
	}
	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	copy(raw[6:], entropy)
	body, err := encodeCrockford(raw)
	if err != nil {
		return "", err
	}
	return PrefixDelivery + body, nil
}

// encodeCrockford encodes 16 bytes as 26 Crockford characters. Two zero bits
// are prefixed so the 128-bit value occupies the low 128 bits of a 130-bit
// base32 string, matching ULID layout without a ULID dependency.
func encodeCrockford(raw [16]byte) (string, error) {
	out := make([]byte, 26)
	var acc uint64
	bits := uint(2)
	n := 0
	for _, c := range raw {
		acc = (acc << 8) | uint64(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[n] = crockfordAlphabet[(acc>>bits)&31]
			n++
		}
	}
	if n != 26 || bits != 0 {
		return "", fmt.Errorf("ingress: crockford width n=%d bits=%d", n, bits)
	}
	return string(out), nil
}
