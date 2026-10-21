package store

import (
	"fmt"

	"github.com/polds/factory/internal/ids"
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

// NewID returns prefix plus 26 Crockford base32 characters encoding a 48-bit
// millisecond timestamp and 80 bits of cryptographic randomness.
func NewID(prefix string) (string, error) {
	if !knownPrefix(prefix) {
		return "", fmt.Errorf("store: unknown id prefix %q", prefix)
	}
	id, err := ids.New(prefix)
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return id, nil
}

// FormatID encodes ms and 10 entropy bytes with prefix. ms must fit in 48 bits.
func FormatID(prefix string, ms uint64, entropy []byte) (string, error) {
	if !knownPrefix(prefix) {
		return "", fmt.Errorf("store: unknown id prefix %q", prefix)
	}
	id, err := ids.Format(prefix, ms, entropy)
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return id, nil
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
