package handoff

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

// PrefixConversation is the conversation id prefix. The body uses the same
// 48-bit timestamp plus 80-bit entropy layout as store run ids.
const PrefixConversation = "conv_"

// crockfordAlphabet is Crockford base32 (no I, L, O, or U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewConversationID returns a new conversation id. It panics if the process
// cannot read cryptographic entropy; that failure is not recoverable here.
func NewConversationID() string {
	var entropy [10]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		panic(fmt.Errorf("handoff: id entropy: %w", err))
	}
	id, err := formatID(PrefixConversation, uint64(time.Now().UnixMilli()), entropy[:])
	if err != nil {
		panic(fmt.Errorf("handoff: conversation id: %w", err))
	}
	return id
}

// formatID encodes ms and 10 entropy bytes with prefix. ms must fit in 48 bits.
// The layout matches store.FormatID. That function rejects a conv_ prefix,
// and this package cannot extend store's prefix set, so the encoder is local.
func formatID(prefix string, ms uint64, entropy []byte) (string, error) {
	if prefix != PrefixConversation {
		return "", fmt.Errorf("handoff: unknown id prefix %q", prefix)
	}
	if ms >= 1<<48 {
		return "", fmt.Errorf("handoff: timestamp does not fit in 48 bits")
	}
	if len(entropy) != 10 {
		return "", fmt.Errorf("handoff: entropy must be 10 bytes")
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
		return "", fmt.Errorf("handoff: crockford width n=%d bits=%d", n, bits)
	}
	return string(out), nil
}
