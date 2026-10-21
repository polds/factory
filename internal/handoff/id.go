package handoff

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/polds/factory/internal/ids"
)

// PrefixConversation is the conversation id prefix. The body uses the same
// 48-bit timestamp plus 80-bit entropy layout as store run ids.
const PrefixConversation = "conv_"

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
// The layout is ids.Format. Only conv_ is accepted here.
func formatID(prefix string, ms uint64, entropy []byte) (string, error) {
	if prefix != PrefixConversation {
		return "", fmt.Errorf("handoff: unknown id prefix %q", prefix)
	}
	id, err := ids.Format(prefix, ms, entropy)
	if err != nil {
		return "", fmt.Errorf("handoff: %w", err)
	}
	return id, nil
}
