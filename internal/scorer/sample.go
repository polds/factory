package scorer

import (
	"crypto/sha256"
	"encoding/binary"
)

// Sampled reports whether runID is in scorerName's sample at ratePercent.
// The bucket is sha256(run_id || 0x00 || scorer) mod 100. Rate 0 never
// samples. Rate 100 or more always samples. The same inputs always match.
func Sampled(runID, scorerName string, ratePercent int) bool {
	if ratePercent <= 0 {
		return false
	}
	if ratePercent >= 100 {
		return true
	}
	return sampleBucket(runID, scorerName) < ratePercent
}

func sampleBucket(runID, scorerName string) int {
	h := sha256.New()
	_, _ = h.Write([]byte(runID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(scorerName))
	sum := h.Sum(nil)
	return int(binary.BigEndian.Uint64(sum[:8]) % 100)
}
