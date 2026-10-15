package ingress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDispatcherMarksOutcomes(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	now := time.Unix(1_700_000_000, 0).UTC()
	seed := func(id string) {
		t.Helper()
		dup, err := store.Enqueue(context.Background(), Delivery{
			ID: id, Provider: ProviderGitHub, DeliveryID: id, ReceivedAt: now, Body: []byte(id),
		})
		if err != nil || dup {
			t.Fatalf("seed %s dup=%v err=%v", id, dup, err)
		}
	}
	seed("matched")
	seed("unmatched")
	seed("ignored")
	seed("failed")

	d := &Dispatcher{Store: store, Limit: 10, Handle: func(_ context.Context, delivery Delivery) (bool, error) {
		switch delivery.ID {
		case "matched":
			return true, nil
		case "unmatched":
			return false, nil
		case "ignored":
			return false, ErrIgnored
		case "failed":
			return false, errors.New("boom")
		default:
			t.Errorf("unexpected %s", delivery.ID)
			return false, nil
		}
	}}
	n, err := d.Poll(context.Background())
	if n != 4 {
		t.Fatalf("claimed %d", n)
	}
	if err == nil || err.Error() == "" {
		t.Fatal("expected the failed delivery's error")
	}
	if !errors.Is(err, ErrIgnored) && store.statusOf("ignored") != StatusIgnored {
		t.Fatalf("ignored status %s err %v", store.statusOf("ignored"), err)
	}
	if errors.Is(err, ErrIgnored) {
		t.Fatal("ErrIgnored was returned from Poll")
	}
	got := map[string]string{
		"matched":   store.statusOf("matched"),
		"unmatched": store.statusOf("unmatched"),
		"ignored":   store.statusOf("ignored"),
		"failed":    store.statusOf("failed"),
	}
	want := map[string]string{
		"matched":   StatusMatched,
		"unmatched": StatusUnmatched,
		"ignored":   StatusIgnored,
		"failed":    StatusFailed,
	}
	for id, status := range want {
		if got[id] != status {
			t.Fatalf("%s status %s", id, got[id])
		}
	}
}

func TestDispatcherRequeuesOnCancel(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	if _, err := store.Enqueue(context.Background(), Delivery{ID: "dlv_a", Provider: ProviderGitHub, DeliveryID: "a"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelling := &cancelOnClaim{mem: store, cancel: cancel}
	handled := 0
	d := &Dispatcher{Store: cancelling, Handle: func(context.Context, Delivery) (bool, error) {
		handled++
		return true, nil
	}}
	if _, err := d.Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("poll: %v", err)
	}
	if handled != 0 {
		t.Fatalf("handled %d", handled)
	}
	if store.statusOf("dlv_a") != StatusReceived {
		t.Fatalf("status %s", store.statusOf("dlv_a"))
	}
	n, err := d.Poll(context.Background())
	if err != nil || n != 1 || handled != 1 {
		t.Fatalf("retry n=%d handled=%d err=%v", n, handled, err)
	}
	if store.statusOf("dlv_a") != StatusMatched {
		t.Fatalf("status %s", store.statusOf("dlv_a"))
	}
}

func TestDispatcherConcurrentClaim(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	const n = 50
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("dlv_%02d", i)
		if _, err := store.Enqueue(context.Background(), Delivery{ID: id, Provider: ProviderGitHub, DeliveryID: id}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	d := &Dispatcher{Store: store, Limit: 3, Handle: func(_ context.Context, delivery Delivery) (bool, error) {
		mu.Lock()
		seen[delivery.ID]++
		mu.Unlock()
		return true, nil
	}}
	errc := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claimed, err := d.Poll(context.Background())
				if err != nil {
					errc <- err
					return
				}
				if claimed == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if len(seen) != n {
		t.Fatalf("seen %d", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s handled %d", id, count)
		}
		if store.statusOf(id) != StatusMatched {
			t.Fatalf("%s status %s", id, store.statusOf(id))
		}
	}
}

type cancelOnClaim struct {
	mem    *memStore
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnClaim) ClaimReceived(ctx context.Context, limit int) ([]Delivery, error) {
	got, err := c.mem.ClaimReceived(ctx, limit)
	c.once.Do(c.cancel)
	return got, err
}

func (c *cancelOnClaim) MarkDelivery(ctx context.Context, id, status string) error {
	return c.mem.MarkDelivery(ctx, id, status)
}
