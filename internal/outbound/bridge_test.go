package outbound

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/store"
)

func TestBridgeDisabledQueuesWithoutSending(t *testing.T) {
	bridge := openBridge(t)
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
	bridge := openBridge(t)
	bridge.SetEnabled(true)
	bridge.SetSendAuthorized(true)
	bridge.SetOutage(true)
	got := bridge.Enqueue(BridgeNotice{IdempotencyKey: "report:outage", Summary: "findings=0"})
	if !got.Queued || got.Sent || got.BlocksExecution || bridge.SendCount() != 0 {
		t.Fatalf("%+v count %d", got, bridge.SendCount())
	}
}

func TestBridgeSendRequiresEnabledAndPolicy(t *testing.T) {
	bridge := openBridge(t)
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

func TestBridgeSecondInstanceDoesNotResend(t *testing.T) {
	st := openBridgeStore(t)
	first := NewBridge()
	first.BindStore(st)
	first.SetEnabled(true)
	first.SetSendAuthorized(true)
	sent := first.Enqueue(BridgeNotice{IdempotencyKey: "report:restart", Summary: "findings=0"})
	if !sent.Sent || sent.SendCount != 1 {
		t.Fatalf("first %+v", sent)
	}
	second := NewBridge()
	second.BindStore(st)
	second.SetEnabled(true)
	second.SetSendAuthorized(true)
	again := second.Enqueue(BridgeNotice{IdempotencyKey: "report:restart"})
	if again.Sent || again.Suppressed != "replay" || !again.ReplaySuppressed || second.SendCount() != 1 {
		t.Fatalf("restart %+v count %d", again, second.SendCount())
	}
	echo := second.Enqueue(BridgeNotice{Binding: sent.Binding})
	if echo.Suppressed != "echo" || echo.Sent || second.SendCount() != 1 {
		t.Fatalf("binding %+v count %d", echo, second.SendCount())
	}
}

func openBridge(t *testing.T) *Bridge {
	t.Helper()
	bridge := NewBridge()
	bridge.BindStore(openBridgeStore(t))
	return bridge
}

func openBridgeStore(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	ctx := context.Background()
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
		_ = admin.Close(context.Background())
	})
	poolDSN, err := bridgeDSN(base, schema)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, poolDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func bridgeDSN(raw, schema string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("pool_max_conns", "4")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
