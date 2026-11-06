package nativescan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/result"
)

const (
	// DefaultSHA is the pinned default-branch revision for both schedule slots.
	DefaultSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// BaseSHA is the pinned pull-request base.
	BaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// HeadSHA is the pinned pull-request head.
	HeadSHA = "cccccccccccccccccccccccccccccccccccccccc"
	// SlotA and SlotB are the two nonoverlapping schedule slots.
	SlotA = "2026-11-02T00:00:00Z"
	SlotB = "2026-11-09T00:00:00Z"

	slotAID   = "slot-2026-11-02t00"
	slotBID   = "slot-2026-11-09t00"
	prInputID = "pr-input"
	plantedID = "planted-item"
	cancelID  = "item-cancel"
	retryID   = "item-retry"
)

// ErrOverlap means a schedule slot started while another slot was still running.
var ErrOverlap = errors.New("nativescan: overlap")

// ErrDefinition means the synthetic definition is not the scanner profile.
var ErrDefinition = errors.New("nativescan: definition")

// Definition is the one scanner profile shared by schedule and pr-input runs.
type Definition struct {
	Name          string   `json:"name"`
	Profile       string   `json:"profile"`
	Requires      []string `json:"requires"`
	ExecuteTarget bool     `json:"executeTarget"`
	Linear        *string  `json:"linear"`
	DefaultSHA    string   `json:"defaultSha"`
	CodeSHA       string   `json:"codeSha"`
	ImageDigest   string   `json:"imageDigest"`
	AdapterDigest string   `json:"adapterDigest"`
}

// ReportView is a published report. It has no pull-request URL.
type ReportView struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	DefinitionDigest string    `json:"definitionDigest"`
	DefaultSHA       string    `json:"defaultSha,omitempty"`
	BaseSHA          string    `json:"baseSha,omitempty"`
	HeadSHA          string    `json:"headSha,omitempty"`
	Slot             string    `json:"slot,omitempty"`
	Findings         int       `json:"findings"`
	Coverage         string    `json:"coverage"`
	Body             string    `json:"body"`
	Link             string    `json:"link"`
	PublishedAt      time.Time `json:"publishedAt"`
}

// Summary is the offline acceptance result.
type Summary struct {
	SlotA            ReportView
	SlotB            ReportView
	PRInput          ReportView
	Planted          ReportView
	ReportAfter      ReportView
	CoverageAfter    string
	SendCount        int
	LinearCalls      int
	CancelRemoved    int
	PRs              int
	RetryCommits     int
	BoundedCleanup   bool
	DefinitionDigest string
}

// Engine is one offline acceptance root.
type Engine struct {
	root        string
	def         Definition
	digest      string
	bridge      *outbound.Bridge
	linearCalls int
	running     bool
	commits     map[string]int
	clock       func() time.Time
}

// LoadDefinition reads the embedded synthetic definition.
func LoadDefinition() (Definition, string, error) {
	raw, err := embedded.ReadFile("fixtures/definition.json")
	if err != nil {
		return Definition{}, "", err
	}
	var def Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return Definition{}, "", err
	}
	if def.Name != "native-scan" || def.Profile != "scanner" || def.ExecuteTarget {
		return Definition{}, "", ErrDefinition
	}
	if def.Linear != nil || len(def.Requires) != 1 || def.Requires[0] != "report" {
		return Definition{}, "", ErrDefinition
	}
	if def.DefaultSHA != DefaultSHA {
		return Definition{}, "", ErrDefinition
	}
	sum := sha256.Sum256(raw)
	return def, hex.EncodeToString(sum[:]), nil
}

// NewEngine loads the definition under root.
func NewEngine(root string, bridge *outbound.Bridge) (*Engine, error) {
	if bridge == nil {
		bridge = outbound.NewBridge()
	}
	def, digest, err := LoadDefinition()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "items"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "retention"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "journal"), 0o755); err != nil {
		return nil, err
	}
	return &Engine{
		root:    root,
		def:     def,
		digest:  digest,
		bridge:  bridge,
		commits: map[string]int{},
	}, nil
}

// RunAcceptance runs both schedule slots, the pr-input scan, cleanup, cancel, and retry.
func RunAcceptance(root string, bridge *outbound.Bridge) (*Engine, Summary, error) {
	engine, err := NewEngine(root, bridge)
	if err != nil {
		return nil, Summary{}, err
	}
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644); err != nil {
		return nil, Summary{}, err
	}
	slotA, err := engine.runSlot(slotAID, SlotA)
	if err != nil {
		return nil, Summary{}, err
	}
	slotB, err := engine.runSlot(slotBID, SlotB)
	if err != nil {
		return nil, Summary{}, err
	}
	prInput, err := engine.runPR()
	if err != nil {
		return nil, Summary{}, err
	}
	planted, err := engine.runPlanted()
	if err != nil {
		return nil, Summary{}, err
	}
	for _, id := range []string{slotAID, slotBID, prInputID, plantedID} {
		if err := engine.RemoveWorkspace(id); err != nil {
			return nil, Summary{}, err
		}
	}
	after, err := engine.Get(slotAID)
	if err != nil {
		return nil, Summary{}, err
	}
	engine.bridge.SetOutage(true)
	queued := engine.bridge.Enqueue(outbound.BridgeNotice{
		IdempotencyKey: "report:" + slotAID,
		TaskRef:        "ntask_01ARZ3NDEKTSV4RRFFQ69G5FAA",
		Summary:        after.Body,
	})
	echo := engine.bridge.Enqueue(outbound.BridgeNotice{
		Origin:  outbound.BridgeOwner,
		Binding: queued.Binding,
		TaskRef: "ntask_01ARZ3NDEKTSV4RRFFQ69G5FAA",
	})
	replay := engine.bridge.Enqueue(outbound.BridgeNotice{
		IdempotencyKey: "report:" + slotAID,
		TaskRef:        "ntask_01ARZ3NDEKTSV4RRFFQ69G5FAA",
	})
	if queued.BlocksExecution || echo.BlocksExecution || replay.BlocksExecution || engine.bridge.SendCount() != 0 {
		return nil, Summary{}, errors.New("nativescan: bridge")
	}
	if queued.Queued && (echo.Suppressed != "echo" || replay.Suppressed != "replay") {
		return nil, Summary{}, errors.New("nativescan: bridge")
	}
	if _, err := engine.Get(slotAID); err != nil {
		return nil, Summary{}, err
	}
	removed, prs, err := engine.Cancel(cancelID)
	if err != nil {
		return nil, Summary{}, err
	}
	if err := engine.CrashBeforeCommit(retryID); err != nil {
		return nil, Summary{}, err
	}
	if err := engine.Retry(retryID); err != nil {
		return nil, Summary{}, err
	}
	if err := engine.Retry(retryID); err != nil {
		return nil, Summary{}, err
	}
	bounded := engine.boundOK()
	return engine, Summary{
		SlotA:            slotA,
		SlotB:            slotB,
		PRInput:          prInput,
		Planted:          planted,
		ReportAfter:      after,
		CoverageAfter:    after.Coverage,
		SendCount:        engine.bridge.SendCount(),
		LinearCalls:      engine.linearCalls,
		CancelRemoved:    removed,
		PRs:              prs,
		RetryCommits:     engine.commits[retryID],
		BoundedCleanup:   bounded,
		DefinitionDigest: engine.digest,
	}, nil
}

func (e *Engine) runSlot(id, slot string) (ReportView, error) {
	if err := e.startSlot(); err != nil {
		return ReportView{}, err
	}
	defer e.finishSlot()
	view := ReportView{
		ID:               id,
		Kind:             "schedule",
		DefinitionDigest: e.digest,
		DefaultSHA:       e.def.DefaultSHA,
		Slot:             slot,
		Findings:         0,
		Coverage:         "recorded",
		Body:             "findings=0",
		Link:             linkFor(id),
	}
	if err := e.prepareWorkspace(id); err != nil {
		return ReportView{}, err
	}
	if err := e.commit(id, view); err != nil {
		return ReportView{}, err
	}
	return view, nil
}

func (e *Engine) runPR() (ReportView, error) {
	view := ReportView{
		ID:               prInputID,
		Kind:             "pr-input",
		DefinitionDigest: e.digest,
		DefaultSHA:       e.def.DefaultSHA,
		BaseSHA:          BaseSHA,
		HeadSHA:          HeadSHA,
		Findings:         0,
		Coverage:         "recorded",
		Body:             "findings=0",
		Link:             linkFor(prInputID),
	}
	if err := e.prepareWorkspace(prInputID); err != nil {
		return ReportView{}, err
	}
	if err := e.commit(prInputID, view); err != nil {
		return ReportView{}, err
	}
	return view, nil
}

func (e *Engine) runPlanted() (ReportView, error) {
	view := ReportView{
		ID:               plantedID,
		Kind:             "planted",
		DefinitionDigest: e.digest,
		DefaultSHA:       e.def.DefaultSHA,
		Findings:         1,
		Coverage:         "recorded",
		Body:             "synthetic-finding",
		Link:             linkFor(plantedID),
	}
	if err := e.prepareWorkspace(plantedID); err != nil {
		return ReportView{}, err
	}
	if err := e.commit(plantedID, view); err != nil {
		return ReportView{}, err
	}
	return view, nil
}

func (e *Engine) startSlot() error {
	if e.running {
		return ErrOverlap
	}
	e.running = true
	return nil
}

func (e *Engine) finishSlot() { e.running = false }

// BeginSlot marks a slot running. A second call without FinishSlot is overlap.
func (e *Engine) BeginSlot() error { return e.startSlot() }

// FinishSlot marks the running slot finished.
func (e *Engine) FinishSlot() { e.finishSlot() }

func (e *Engine) prepareWorkspace(id string) error {
	dir, err := e.itemPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "workspace", "task.txt"), []byte(id), 0o644)
}

func (e *Engine) commit(id string, view ReportView) error {
	if e.commits[id] > 0 {
		return nil
	}
	if err := result.Publish(true, "", result.Report{
		Summary:        view.Body,
		CoverageStatus: view.Coverage,
		FindingsCount:  view.Findings,
		Body:           []byte(view.Body),
	}); err != nil {
		return err
	}
	dir, err := e.retentionDir(id)
	if err != nil {
		return err
	}
	view.PublishedAt = e.now()
	raw, err := json.Marshal(view)
	if err != nil {
		return err
	}
	path, err := RetainedReportPath(e.root, id)
	if err != nil {
		return err
	}
	if err := writeRetainedReport(path, raw); err != nil {
		return err
	}
	coverage, err := json.Marshal(map[string]string{"status": view.Coverage})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "coverage.json"), coverage, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "link.txt"), []byte(view.Link), 0o644); err != nil {
		return err
	}
	journal, err := e.journalPath(id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(journal, []byte("committed"), 0o644); err != nil {
		return err
	}
	e.commits[id]++
	return nil
}

func (e *Engine) now() time.Time {
	if e != nil && e.clock != nil {
		return e.clock().UTC()
	}
	return time.Now().UTC()
}

// Get loads a retained report. It does not read the item workspace.
func (e *Engine) Get(id string) (ReportView, error) {
	dir, err := e.retentionDir(id)
	if err != nil {
		return ReportView{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return ReportView{}, err
	}
	var view ReportView
	if err := json.Unmarshal(raw, &view); err != nil {
		return ReportView{}, err
	}
	return view, nil
}

// RemoveWorkspace deletes one item directory and leaves retention in place.
func (e *Engine) RemoveWorkspace(id string) error {
	dir, err := e.itemPath(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

// Cancel removes a started item without publishing and without opening a pull request.
func (e *Engine) Cancel(id string) (removed int, prs int, err error) {
	if err := e.prepareWorkspace(id); err != nil {
		return 0, 0, err
	}
	if err := e.RemoveWorkspace(id); err != nil {
		return 0, 0, err
	}
	if _, err := e.Get(id); err == nil {
		return 0, 0, errors.New("nativescan: cancel published")
	}
	matches, err := filepath.Glob(filepath.Join(e.root, "pull-request*"))
	if err != nil {
		return 0, 0, err
	}
	return 1, len(matches), nil
}

// CrashBeforeCommit leaves a partial workspace and no retained report.
func (e *Engine) CrashBeforeCommit(id string) error {
	if err := e.prepareWorkspace(id); err != nil {
		return err
	}
	journal, err := e.journalPath(id)
	if err != nil {
		return err
	}
	return os.WriteFile(journal, []byte("open"), 0o644)
}

// Retry commits an open journal once. A committed journal is a replay.
func (e *Engine) Retry(id string) error {
	journal, err := e.journalPath(id)
	if err != nil {
		return err
	}
	state, err := os.ReadFile(journal)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(state)) == "committed" {
		return nil
	}
	view := ReportView{
		ID:               id,
		Kind:             "retry",
		DefinitionDigest: e.digest,
		DefaultSHA:       e.def.DefaultSHA,
		Findings:         0,
		Coverage:         "recorded",
		Body:             "findings=0",
		Link:             linkFor(id),
	}
	return e.commit(id, view)
}

func (e *Engine) journalPath(id string) (string, error) {
	if !itemIDRe.MatchString(id) {
		return "", ErrItemID
	}
	return filepath.Join(e.root, "journal", id), nil
}

func (e *Engine) boundOK() bool {
	keep := filepath.Join(e.root, "keep.txt")
	before, err := os.ReadFile(keep)
	if err != nil {
		return false
	}
	if err := e.RemoveWorkspace("../keep.txt"); err == nil {
		return false
	}
	after, err := os.ReadFile(keep)
	if err != nil {
		return false
	}
	return string(before) == string(after)
}

func linkFor(id string) string {
	return "/api/v1/scans/" + id + "/report"
}

// FormatSummary is the stable driver text.
func FormatSummary(sum Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "m10 schedule slot=%s sha=%s findings=%d\n", sum.SlotA.Slot, sum.SlotA.DefaultSHA, sum.SlotA.Findings)
	fmt.Fprintf(&b, "m10 schedule slot=%s sha=%s findings=%d\n", sum.SlotB.Slot, sum.SlotB.DefaultSHA, sum.SlotB.Findings)
	fmt.Fprintf(&b, "m10 pr-input base=%s head=%s findings=%d\n", sum.PRInput.BaseSHA, sum.PRInput.HeadSHA, sum.PRInput.Findings)
	fmt.Fprintf(&b, "m10 planted retrieved after cleanup id=%s findings=%d\n", sum.Planted.ID, sum.Planted.Findings)
	fmt.Fprintf(&b, "m10 report retrieved after cleanup id=%s findings=%d coverage=%s\n", sum.ReportAfter.ID, sum.ReportAfter.Findings, sum.CoverageAfter)
	fmt.Fprintf(&b, "m10 bridge sendCount=%d\n", sum.SendCount)
	fmt.Fprintf(&b, "m10 linear-off calls=%d\n", sum.LinearCalls)
	fmt.Fprintf(&b, "m10 cancel removed=%d prs=%d\n", sum.CancelRemoved, sum.PRs)
	fmt.Fprintf(&b, "m10 retry commits=%d\n", sum.RetryCommits)
	if sum.BoundedCleanup {
		fmt.Fprintf(&b, "m10 cleanup bounded=1\n")
	}
	fmt.Fprintf(&b, "m10 accept PASS\n")
	return b.String()
}
