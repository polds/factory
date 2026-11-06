package linear

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSyntheticNativeBrief(t *testing.T) {
	const taskRef = "ntask_01ARZ3NDEKTSV4RRFFQ69G5FAA"
	got, err := SyntheticNativeBrief(taskRef)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "native" || got.TaskRef != taskRef || got.LinearIssue != nil || got.RequiresLinear {
		t.Fatalf("%+v", got)
	}
	for _, id := range []string{"", "11111111-1111-4111-8111-111111111111", "linear_issue", "ntask_short"} {
		if _, err := SyntheticNativeBrief(id); !errors.Is(err, ErrNativeBrief) {
			t.Fatalf("%s err %v", id, err)
		}
	}
	body := compactFile(t, filepath.Join("testdata", EventIssueCreated, "input.json"))
	evs, err := Normalize(Delivery{Provider: Provider, DeliveryID: fixtureDeliveryID, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Thread.Kind != ThreadKind {
		t.Fatalf("normalize thread %+v", evs)
	}
	if ThreadKind != "linear_issue" {
		t.Fatal(ThreadKind)
	}
}
