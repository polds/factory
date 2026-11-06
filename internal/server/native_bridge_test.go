package server

import (
	"context"
	"errors"
	"testing"

	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/outbound"
)

func TestRouteBridgeEcho(t *testing.T) {
	if RouteBridgeEcho(ingress.Delivery{}) {
		t.Fatal("empty delivery")
	}
	plain := ingress.Delivery{Headers: map[string]string{"X-Github-Event": "issues"}}
	if RouteBridgeEcho(plain) {
		t.Fatal("github delivery")
	}
	echo := ingress.Delivery{Headers: map[string]string{
		"X-Factory-Bridge-Origin": outbound.BridgeOwner,
		"X-Factory-Bridge-Echo":   "1",
	}}
	if !RouteBridgeEcho(echo) {
		t.Fatal("echo")
	}
	server := &Server{}
	matched, err := server.handleDelivery(context.Background(), echo)
	if matched || !errors.Is(err, ingress.ErrIgnored) {
		t.Fatalf("matched %v err %v", matched, err)
	}
}

func TestReportThroughBridge(t *testing.T) {
	bridge := outbound.NewBridge()
	bridge.SetOutage(true)
	published := false
	err := ReportThroughBridge(bridge, outbound.BridgeNotice{IdempotencyKey: "report:native", Summary: "findings=0"}, func() error {
		published = true
		return nil
	})
	if err != nil || !published || bridge.SendCount() != 0 {
		t.Fatalf("err %v published %v count %d", err, published, bridge.SendCount())
	}
	if err := ReportThroughBridge(bridge, outbound.BridgeNotice{}, nil); err == nil {
		t.Fatal("nil publish")
	}
}
