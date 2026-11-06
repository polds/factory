package result_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/polds/factory/internal/result"
)

func TestINXPathRejectsAbsolute(t *testing.T) {
	if err := result.ValidatePath("/etc/passwd"); err == nil {
		t.Fatal("expected reject")
	}
	var pub result.PublishError
	if err := result.ValidatePath("/tmp/x"); !errorsAs(err, &pub) || pub.Code != result.CodePathRejected {
		t.Fatalf("err %v", err)
	}
}

func TestINVSizeRejectsOversizeReport(t *testing.T) {
	body := make([]byte, (1<<20)+1)
	err := result.ValidateSize(body, nil, "")
	var pub result.PublishError
	if !errorsAs(err, &pub) || pub.Code != result.CodeOversize {
		t.Fatalf("err %v", err)
	}
}

func TestINVSymlinkRejectsLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "report.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink unsupported")
	}
	err := result.ValidateSymlink(root, "report.md")
	var pub result.PublishError
	if !errorsAs(err, &pub) || pub.Code != result.CodeSymlinkRejected {
		t.Fatalf("err %v", err)
	}
}

func TestINVSecretPubRejectsCanary(t *testing.T) {
	err := result.ScanSecrets([]byte("hello CANARY-SYNTHETIC"))
	var pub result.PublishError
	if !errorsAs(err, &pub) || pub.Code != result.CodeSecretRejected {
		t.Fatalf("err %v", err)
	}
}

func errorsAs(err error, target *result.PublishError) bool {
	var pub result.PublishError
	if !errors.As(err, &pub) {
		return false
	}
	*target = pub
	return true
}
