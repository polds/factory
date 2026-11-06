package secrets_test

import (
	"context"
	"testing"

	"github.com/polds/factory/internal/secrets"
)

func TestGatewayFailClosed(t *testing.T) {
	var alloc secrets.FailClosedAllocator
	_, err := alloc.AllocateEngagement(context.Background(), "eng-1", 2500)
	if err == nil {
		t.Fatal("expected error")
	}
	if !secrets.IsGatewayUnavailable(err) {
		t.Fatalf("got %v", err)
	}
	if alloc.GuaranteesCeiling() {
		t.Fatal("stub must not claim guaranteesCeiling")
	}
}
