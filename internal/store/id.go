package store

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

const (
	PrefixRun            = "run_"
	PrefixExec           = "exec_"
	PrefixFactory        = "fac_"
	PrefixAgent          = "agt_"
	PrefixDefinition     = "def_"
	PrefixWorkItem       = "task_"
	PrefixApproval       = "apr_"
	PrefixConversation   = "conv_"
	PrefixDelivery       = "dlv_"
	PrefixSchedule       = "sch_"
	PrefixInputRequest   = "inr_"
	PrefixWebhook        = "whk_"
	PrefixSteer          = "str_"
	PrefixAutomation     = "aut_"
	PrefixScorerResult   = "scr_"
	PrefixImproveBatch   = "ibt_"
	PrefixBenchmarkRun   = "bmr_"
	PrefixBenchmarkTrial = "bmt_"
)

// crockfordAlphabet is Crockford base32 (no I, L, O, or U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns prefix plus 26 Crockford base32 characters encoding a 48-bit
// millisecond timestamp and 80 bits of cryptographic randomness.
func NewID(prefix string) (string, error) {
	var entropy [10]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("store: id entropy: %w", err)
	}
	return FormatID(prefix, uint64(time.Now().UnixMilli()), entropy[:])
}

// FormatID encodes ms and 10 entropy bytes with prefix. ms must fit in 48 bits.
func FormatID(prefix string, ms uint64, entropy []byte) (string, error) {
	if !knownPrefix(prefix) {
		return "", fmt.Errorf("store: unknown id prefix %q", prefix)
	}
	if ms >= 1<<48 {
		return "", fmt.Errorf("store: timestamp does not fit in 48 bits")
	}
	if len(entropy) != 10 {
		return "", fmt.Errorf("store: entropy must be 10 bytes")
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

func knownPrefix(prefix string) bool {
	switch prefix {
	case PrefixRun, PrefixExec, PrefixFactory, PrefixAgent, PrefixDefinition,
		PrefixWorkItem, PrefixApproval, PrefixConversation,
		PrefixDelivery, PrefixSchedule, PrefixInputRequest, PrefixWebhook,
		PrefixSteer, PrefixAutomation, PrefixScorerResult, PrefixImproveBatch,
		PrefixBenchmarkRun, PrefixBenchmarkTrial:
		return true
	default:
		return false
	}
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
		return "", fmt.Errorf("store: crockford width n=%d bits=%d", n, bits)
	}
	return string(out), nil
}
