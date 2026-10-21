// Package ids encodes a 48-bit millisecond timestamp and 80 bits of entropy
// as 26 Crockford base32 characters. It does not validate prefixes. Callers
// keep their own prefix rule and error prefix.
package ids

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

// crockfordAlphabet is Crockford base32 (no I, L, O, or U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Format returns prefix plus 26 Crockford characters.
// ms must fit in 48 bits and entropy must be 10 bytes.
// Errors have no package prefix. Callers wrap them.
func Format(prefix string, ms uint64, entropy []byte) (string, error) {
	if ms >= 1<<48 {
		return "", fmt.Errorf("timestamp does not fit in 48 bits")
	}
	if len(entropy) != 10 {
		return "", fmt.Errorf("entropy must be 10 bytes")
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
	return prefix + body, nil
}

// New returns prefix plus a new id. The clock is time.Now and the entropy
// is cryptographic. store.NewID is the caller. Ingress passes its own clock
// to Format, and handoff panics through its own entropy read, so neither
// uses New.
func New(prefix string) (string, error) {
	var entropy [10]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("id entropy: %w", err)
	}
	return Format(prefix, uint64(time.Now().UnixMilli()), entropy[:])
}

// encodeCrockford encodes 16 bytes as 26 Crockford characters. Two zero bits
// are prefixed so the 128-bit value occupies the low 128 bits of a 130-bit
// base32 string, matching ULID layout without taking a ULID dependency.
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
		return "", fmt.Errorf("crockford width n=%d bits=%d", n, bits)
	}
	return string(out), nil
}
