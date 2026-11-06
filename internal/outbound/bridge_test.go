package outbound

import "testing"

func TestBridgeDisabledQueuesWithoutSending(t *testing.T) {
	bridge := NewBridge()
	if bridge.Enabled() || bridge.SendAuthorized() || len(bridge.Owners()) != 1 || bridge.Owners()[0] != BridgeOwner {
		t.Fatalf("enabled=%v authorized=%v owners=%v", bridge.Enabled(), bridge.SendAuthorized(), bridge.Owners())
	}
	first := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:1", TaskRef: "ntask_01ARZ3NDEKTSV4RRFFQ69G5FAA", Summary: "findings=0"})
	if !first.Queued || first.Sent || first.BlocksExecution || first.SendCount != 0 || first.OutageQueues != "reporting" {
		t.Fatalf("first %+v", first)
	}
	echo := bridge.Enqueue(BridgeNotice{Origin: BridgeOwner, Binding: first.Binding})
	if echo.Suppressed != "echo" || !echo.EchoSuppressed || echo.Sent || echo.BlocksExecution {
		t.Fatalf("echo %+v", echo)
	}
	replay := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:1"})
	if replay.Suppressed != "replay" || !replay.ReplaySuppressed || replay.Sent {
		t.Fatalf("replay %+v", replay)
	}
	if bridge.SendCount() != 0 {
		t.Fatal(bridge.SendCount())
	}
}

func TestBridgeOutageDoesNotBlock(t *testing.T) {
	bridge := NewBridge()
	bridge.SetEnabled(true)
	bridge.SetSendAuthorized(true)
	bridge.SetOutage(true)
	got := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:outage", Summary: "findings=0"})
	if !got.Queued || got.Sent || got.BlocksExecution || bridge.SendCount() != 0 {
		t.Fatalf("%+v count %d", got, bridge.SendCount())
	}
}

func TestBridgeSendRequiresEnabledAndPolicy(t *testing.T) {
	bridge := NewBridge()
	bridge.SetSendAuthorized(true)
	held := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:held"})
	if held.Sent || bridge.SendCount() != 0 {
		t.Fatalf("policy without enable %+v", held)
	}
	bridge.SetEnabled(true)
	sent := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:sent"})
	if !sent.Sent || bridge.SendCount() != 1 {
		t.Fatalf("send %+v count %d", sent, bridge.SendCount())
	}
}
