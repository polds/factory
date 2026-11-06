package server

import (
	"errors"

	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/outbound"
)

const (
	bridgeOriginHeader = "X-Factory-Bridge-Origin"
	bridgeEchoHeader   = "X-Factory-Bridge-Echo"
)

// RouteBridgeEcho reports a delivery that is an echo of factory-outbound-bridge.
// Other deliveries return false and keep the existing ingress path.
func RouteBridgeEcho(d ingress.Delivery) bool {
	if d.Headers == nil {
		return false
	}
	return d.Headers[bridgeOriginHeader] == outbound.BridgeOwner && d.Headers[bridgeEchoHeader] == "1"
}

// ReportThroughBridge publishes the native result first. Bridge outage, echo,
// and a disabled owner do not fail that publication.
func ReportThroughBridge(b *outbound.Bridge, n outbound.BridgeNotice, publish func() error) error {
	if publish == nil {
		return errors.New("server: native bridge publish is nil")
	}
	if err := publish(); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	b.Enqueue(n)
	return nil
}
