package webhooks

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBodyDeliveryIDBucket(t *testing.T) {
	t.Parallel()
	aligned := time.Unix((1_700_000_000/300)*300, 0).UTC()
	body := []byte(`{"action":"opened"}`)
	first, err := DeliveryID("", nil, body, aligned)
	if err != nil {
		t.Fatal(err)
	}
	same, err := DeliveryID("", nil, body, aligned.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first != same {
		t.Fatalf("same bucket %s vs %s", first, same)
	}
	next, err := DeliveryID("", nil, body, aligned.Add(DedupBucket))
	if err != nil {
		t.Fatal(err)
	}
	if next == first || !strings.Contains(first, "/") {
		t.Fatalf("bucket ids %s %s", first, next)
	}
	other, err := DeliveryID("", nil, []byte(`{"action":"closed"}`), aligned)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("different bodies shared an id")
	}
}

func TestDeliveryIDUsesHeaderAndRejectsSignatureNames(t *testing.T) {
	t.Parallel()
	h := make(http.Header)
	h.Set("X-CI-Run-Id", " run-9 ")
	got, err := DeliveryID("X-CI-Run-Id", h, []byte("body"), time.Unix(10, 0))
	if err != nil || got != "run-9" {
		t.Fatalf("id %q err %v", got, err)
	}
	if _, err := DeliveryID("X-CI-Run-Id", nil, []byte("body"), time.Unix(10, 0)); err == nil {
		t.Fatal("missing header value")
	}
	for _, name := range []string{"Authorization", "X-Gitlab-Token", "Proxy-Authorization", "X-Webhook-Secret"} {
		if _, err := DeliveryID(name, h, []byte("body"), time.Unix(10, 0)); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if err := ValidateDeliveryIDHeader(strings.Repeat("A", 64)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeliveryIDHeader(""); err != nil {
		t.Fatal(err)
	}
}
