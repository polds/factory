package nativescan

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/result"
)

func TestM10Acceptance(t *testing.T) {
	bridge := outbound.NewBridge()
	root := t.TempDir()
	engine, sum, err := RunAcceptance(root, bridge)
	if err != nil {
		t.Fatal(err)
	}
	if sum.SlotA.DefinitionDigest == "" || sum.SlotA.DefinitionDigest != sum.SlotB.DefinitionDigest || sum.SlotA.DefinitionDigest != sum.PRInput.DefinitionDigest || sum.SlotA.DefinitionDigest != sum.Planted.DefinitionDigest {
		t.Fatalf("digest slot=%s pr=%s planted=%s", sum.SlotA.DefinitionDigest, sum.PRInput.DefinitionDigest, sum.Planted.DefinitionDigest)
	}
	if sum.SlotA.DefaultSHA != DefaultSHA || sum.SlotB.DefaultSHA != DefaultSHA || sum.SlotA.Slot != SlotA || sum.SlotB.Slot != SlotB {
		t.Fatalf("slots %+v %+v", sum.SlotA, sum.SlotB)
	}
	if sum.PRInput.BaseSHA != BaseSHA || sum.PRInput.HeadSHA != HeadSHA || sum.PRInput.Findings != 0 {
		t.Fatalf("pr-input %+v", sum.PRInput)
	}
	if sum.Planted.Findings != 1 || sum.Planted.Body != "synthetic-finding" {
		t.Fatalf("planted %+v", sum.Planted)
	}
	if sum.ReportAfter.Findings != 0 || sum.CoverageAfter != "recorded" || sum.SendCount != 0 || sum.LinearCalls != 0 || sum.PRs != 0 || sum.CancelRemoved != 1 || sum.RetryCommits != 1 || !sum.BoundedCleanup {
		t.Fatalf("summary %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(root, "items", slotAID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
	if _, err := engine.Get(slotAID); err != nil {
		t.Fatal(err)
	}
	text := FormatSummary(sum)
	for _, line := range []string{
		"m10 schedule slot=2026-11-02T00:00:00Z sha=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb findings=0",
		"m10 schedule slot=2026-11-09T00:00:00Z sha=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb findings=0",
		"m10 pr-input base=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa head=cccccccccccccccccccccccccccccccccccccccc findings=0",
		"m10 planted retrieved after cleanup id=planted-item findings=1",
		"m10 report retrieved after cleanup id=slot-2026-11-02t00 findings=0 coverage=recorded",
		"m10 bridge sendCount=0",
		"m10 linear-off calls=0",
		"m10 cancel removed=1 prs=0",
		"m10 retry commits=1",
		"m10 cleanup bounded=1",
		"m10 accept PASS",
	} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %s\n%s", line, text)
		}
	}
	if strings.Contains(text, "CANARY") {
		t.Fatal("summary contains a canary")
	}
}

func TestM10OverlapSkipsSecondSlot(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), outbound.NewBridge())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSlot(); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSlot(); !errors.Is(err, ErrOverlap) {
		t.Fatalf("err %v", err)
	}
	engine.FinishSlot()
	if err := engine.BeginSlot(); err != nil {
		t.Fatal(err)
	}
}

func TestM10ReportAPIAfterCleanup(t *testing.T) {
	root := t.TempDir()
	engine, _, err := RunAcceptance(root, outbound.NewBridge())
	if err != nil {
		t.Fatal(err)
	}
	const token = "m10-offline-token"
	srv := httptest.NewServer(engine.Handler(token))
	t.Cleanup(srv.Close)
	denied := get(t, srv.URL+"/api/v1/scans/"+slotAID+"/report", "")
	if denied.StatusCode != http.StatusForbidden || denied.Body != "" {
		t.Fatalf("denied %d %q", denied.StatusCode, denied.Body)
	}
	ok := get(t, srv.URL+"/api/v1/scans/"+slotAID+"/report", token)
	if ok.StatusCode != http.StatusOK || !strings.Contains(ok.Body, `"findings":0`) || strings.Contains(ok.Body, "CANARY") {
		t.Fatalf("report %d %s", ok.StatusCode, ok.Body)
	}
	coverage := get(t, srv.URL+"/api/v1/scans/"+slotAID+"/coverage", token)
	if coverage.StatusCode != http.StatusOK || !strings.Contains(coverage.Body, `"status":"recorded"`) {
		t.Fatalf("coverage %d %s", coverage.StatusCode, coverage.Body)
	}
	planted := get(t, srv.URL+"/api/v1/scans/planted-item/report", token)
	if planted.StatusCode != http.StatusOK || !strings.Contains(planted.Body, "synthetic-finding") {
		t.Fatalf("planted %d %s", planted.StatusCode, planted.Body)
	}
}

func TestM10PublicationFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Symlink(filepath.Join(root, "note.txt"), link); err != nil {
		t.Fatal(err)
	}
	if err := PublishCandidate(root, true, []byte("ok"), []string{"linked.txt"}); err == nil || !codeIs(err, result.CodeSymlinkRejected) {
		t.Fatalf("symlink %v", err)
	}
	if err := PublishCandidate(root, true, []byte("ok"), []string{"../outside"}); err == nil || !codeIs(err, result.CodePathRejected) {
		t.Fatalf("traversal %v", err)
	}
	big := filepath.Join(root, "big.bin")
	if err := os.WriteFile(big, make([]byte, (1<<20)*16+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PublishCandidate(root, true, []byte("ok"), []string{"big.bin"}); err == nil || !codeIs(err, result.CodeOversize) {
		t.Fatalf("size %v", err)
	}
	if err := PublishCandidate(root, true, []byte("CANARY-SYNTHETIC"), nil); err == nil || !codeIs(err, result.CodeSecretRejected) {
		t.Fatalf("leak %v", err)
	}
	if err := PublishCandidate(root, false, []byte("ok"), nil); err == nil || !codeIs(err, result.CodeUnauthorized) {
		t.Fatalf("auth %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "retention", "*", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("published after failure: %v", matches)
	}
}

func TestM10SampleAndCompose(t *testing.T) {
	sample, err := LoadSample()
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Items) != 2 || sample.DockerSocket || sample.SharedArtRoot || sample.SharedWritableCheckout {
		t.Fatalf("sample %+v", sample)
	}
	sample.DockerSocket = true
	if err := ValidateSample(sample); err == nil {
		t.Fatal("socket accepted")
	}
	text, err := ComposeText()
	if err != nil {
		t.Fatal(err)
	}
	if err := ComposeRefusesInsecure(text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "168h") || !strings.Contains(text, "network_mode: none") {
		t.Fatal("compose retention or network note missing")
	}
}

func codeIs(err error, code string) bool {
	var pub result.PublishError
	return errors.As(err, &pub) && pub.Code == code
}

type response struct {
	StatusCode int
	Body       string
}

func get(t *testing.T, url, token string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{StatusCode: res.StatusCode, Body: string(body)}
}
