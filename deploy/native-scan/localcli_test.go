package nativescan

import (
	"errors"
	"testing"
)

func TestM10LocalCLI(t *testing.T) {
	got, err := RunLocalCLI(LocalCLIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeLocalCLI || got.Findings != 0 || got.TaskRefRequired || got.LinearRequired {
		t.Fatalf("%+v", got)
	}
	if _, err := RunLocalCLI(LocalCLIRequest{TaskRef: ""}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []LocalCLIRequest{
		{RequiresLinear: true},
		{RequiresTaskRef: true},
		{LinearConfigured: true},
	} {
		if _, err := RunLocalCLI(req); !errors.Is(err, ErrLocalCLI) {
			t.Fatalf("%+v err %v", req, err)
		}
	}
}
