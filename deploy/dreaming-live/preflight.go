package dreaminglive

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
)

const (
	DreamingRepo  = "northstar-ai-labs/dreaming"
	RefPrefix     = "factory/dreaming-454-"
	DreamingPin   = "a18fbb6b0e29442872f09e704be996208d0e6e8d"
	ModelID       = "nous/claude-sonnet-5.5"
	ConfirmValue  = "dreaming-live-454"
	CeilingCents  = int64(2500)
	WMaxCents     = int64(250)
	D6A           = "32bf972169c354d7f418e86178e43a24cdcfaf16"
	D6B           = "85a4b843a7b7117e9f507ad1c0f6cef9b5d89686"
	OutsideReport = "tobias-lindqvist"

	checkSecrets   = "P1"
	checkPin       = "P2"
	checkApp       = "P3"
	checkRead      = "P4"
	checkRoles     = "P5"
	checkTree      = "P6"
	checkEvidence  = "P7"
	checkBudget    = "P8"
	checkModel     = "P9"
	checkPublish   = "P10"
	checkKill      = "P11"
	checkGuards    = "P12"
	checkIsolation = "P13"
	checkPrior     = "P14"
)

// Row is one preflight result. Detail never holds a secret, a token, or role-file bytes.
type Row struct {
	Check  string `json:"check"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// Report is the preflight.json document.
type Report struct {
	Rows []Row `json:"rows"`
}

// Passed is true when every row passed.
func (r Report) Passed() bool {
	if len(r.Rows) != 14 {
		return false
	}
	for _, row := range r.Rows {
		if !row.Pass {
			return false
		}
	}
	return true
}

// Row returns the named check.
func (r Report) Row(check string) (Row, bool) {
	for _, row := range r.Rows {
		if row.Check == check {
			return row, true
		}
	}
	return Row{}, false
}

// World is the evidence Evaluate reads. It does not carry secret values.
type World struct {
	Needles []string

	Workflow string
	Secrets  map[string]bool

	Head           string
	FactorySHA     string
	Shallow        bool
	Ancestors      map[string]bool
	D6C            string
	FetchDepthZero bool

	AppStatus       int
	AppSlug         string
	InstallationID  int64
	AppPerms        map[string]string
	SelectionCovers bool

	ReadPerms       map[string]string
	ReadRepos       []string
	ReadTTL         time.Duration
	PinExists       bool
	IssueOpen       bool
	IssueTitle      string
	DefaultBranch   string
	PinIsAncestor   bool
	ReadTokenMinted bool

	RoleSource     string
	WantRoleSource string
	CheckoutHead   string
	Pin            string
	BlobGot        map[string]string
	BlobWant       map[string]string

	Tree *definition.NativeTree

	Tests           map[string]string
	EvidencePackage bool
	SentinelAtX     bool

	Price      harness.SpendPrice
	FallbackOK bool

	ModelStatus int
	ModelListed bool
	Now         time.Time

	PublishRepo string
	SecretNames []string
	Purposes    []string

	Allowlist string

	Confirm string
	Mode    string

	Reexec     bool
	SecretHits int

	LsRemoteOK  bool
	RemoteRefs  []string
	PullsStatus int
	PullHeads   []string
	DraftRows   int
	AuthFormOK  bool
}

// Evaluate runs P1–P14. It does not dial.
func Evaluate(w World) Report {
	rows := []Row{
		checkP1(w),
		checkP2(w),
		checkP3(w),
		checkP4(w),
		checkP5(w),
		checkP6(w),
		checkP7(w),
		checkP8(w),
		checkP9(w),
		checkP10(w),
		checkP11(w),
		checkP12(w),
		checkP13(w),
		checkP14(w),
	}
	for i := range rows {
		rows[i].Detail = scrubDetail(rows[i].Detail, w.Needles)
	}
	return Report{Rows: rows}
}

func checkP1(w World) Row {
	names := []string{"M1_OPENAI_BASE_URL", "M1_OPENAI_API_KEY", "M6_GITHUB_APP_ID", "M6_GITHUB_APP_PRIVATE_KEY"}
	for _, name := range names {
		if !w.Secrets[name] {
			return Row{Check: checkSecrets, Detail: "missing " + name}
		}
	}
	if workflowForbidden(w.Workflow) {
		return Row{Check: checkSecrets, Detail: "workflow references a forbidden secret"}
	}
	return Row{Check: checkSecrets, Pass: true, Detail: "present"}
}

func checkP2(w World) Row {
	if !w.FetchDepthZero || !strings.Contains(w.Workflow, "fetch-depth: 0") {
		return Row{Check: checkPin, Detail: "fetch-depth is not 0"}
	}
	if w.Shallow {
		return Row{Check: checkPin, Detail: "ancestry unknown"}
	}
	if w.Head == "" || w.Head != w.FactorySHA {
		return Row{Check: checkPin, Detail: "HEAD is not factory_sha"}
	}
	for _, sha := range []string{D6A, D6B, w.D6C} {
		if sha == "" {
			return Row{Check: checkPin, Detail: "ancestry unknown"}
		}
		ok, known := w.Ancestors[sha]
		if !known || !ok {
			return Row{Check: checkPin, Detail: "ancestry unknown"}
		}
	}
	return Row{Check: checkPin, Pass: true, Detail: "HEAD is factory_sha and D6 commits are ancestors"}
}

func checkP3(w World) Row {
	if w.AppStatus != 200 {
		return Row{Check: checkApp, Detail: fmt.Sprintf("installation HTTP %d", w.AppStatus)}
	}
	if w.InstallationID <= 0 || w.AppSlug == "" {
		return Row{Check: checkApp, Detail: "installation id or slug missing"}
	}
	if w.AppPerms["contents"] != "write" || w.AppPerms["pull_requests"] != "write" || w.AppPerms["metadata"] != "read" {
		return Row{Check: checkApp, Detail: "installation permissions"}
	}
	if !w.SelectionCovers {
		return Row{Check: checkApp, Detail: "selection misses dreaming"}
	}
	return Row{Check: checkApp, Pass: true, Detail: fmt.Sprintf("slug %s installation %d", w.AppSlug, w.InstallationID)}
}

func checkP4(w World) Row {
	if !w.ReadTokenMinted {
		return Row{Check: checkRead, Detail: "read token was not minted"}
	}
	want := map[string]string{
		"contents": "read", "metadata": "read", "issues": "read", "pull_requests": "read",
	}
	if len(w.ReadPerms) != len(want) {
		return Row{Check: checkRead, Detail: "read token permissions"}
	}
	for name, level := range want {
		if w.ReadPerms[name] != level {
			return Row{Check: checkRead, Detail: "read token permissions"}
		}
	}
	if len(w.ReadRepos) != 1 || w.ReadRepos[0] != "dreaming" {
		return Row{Check: checkRead, Detail: "read token repos"}
	}
	if w.ReadTTL <= 0 || w.ReadTTL > time.Hour {
		return Row{Check: checkRead, Detail: "read token expiry"}
	}
	if !w.PinExists {
		return Row{Check: checkRead, Detail: "pin unknown"}
	}
	if !w.IssueOpen {
		return Row{Check: checkRead, Detail: "issue closed"}
	}
	if w.IssueTitle == "" || w.DefaultBranch == "" || !w.PinIsAncestor {
		return Row{Check: checkRead, Detail: "issue, default branch, or pin ancestry"}
	}
	return Row{Check: checkRead, Pass: true, Detail: "issue open title " + w.IssueTitle + " branch " + w.DefaultBranch}
}

func checkP5(w World) Row {
	if w.RoleSource == "" || w.WantRoleSource == "" || w.RoleSource != w.WantRoleSource {
		return Row{Check: checkRoles, Detail: "FACTORY_ROLE_SOURCE unset"}
	}
	if w.CheckoutHead == "" || w.CheckoutHead != w.Pin {
		return Row{Check: checkRoles, Detail: "checkout HEAD is not the pin"}
	}
	if len(w.BlobWant) != 17 {
		return Row{Check: checkRoles, Detail: "role_blobs count"}
	}
	for path, want := range w.BlobWant {
		if w.BlobGot[path] != want || want == "" {
			return Row{Check: checkRoles, Detail: "blob id differs " + path}
		}
	}
	return Row{Check: checkRoles, Pass: true, Detail: "17 blob ids match and FACTORY_ROLE_SOURCE is set"}
}

func checkP6(w World) Row {
	detail, ok := frozenTree(w.Tree)
	return Row{Check: checkTree, Pass: ok, Detail: detail}
}

func checkP7(w World) Row {
	names := []string{
		"TestProducedHeadReviewerPipeline",
		"TestDraftPublishRequiresReviewerPass",
		"TestRunSpendHardStop",
		"TestHarnessSecretsNotInProc",
	}
	for _, name := range names {
		switch w.Tests[name] {
		case "pass":
		case "skip":
			return Row{Check: checkEvidence, Detail: name + " skipped"}
		default:
			return Row{Check: checkEvidence, Detail: name + " did not pass"}
		}
	}
	if !w.EvidencePackage {
		return Row{Check: checkEvidence, Detail: "package missing at pin"}
	}
	if !w.SentinelAtX {
		return Row{Check: checkEvidence, Detail: "sentinel absent at X"}
	}
	return Row{Check: checkEvidence, Pass: true, Detail: "named tests passed and the sentinel is at X"}
}

func checkP8(w World) Row {
	if !w.FallbackOK {
		return Row{Check: checkBudget, Detail: "fallback list missing"}
	}
	cents, ok := w.Price.WorstCaseCents()
	if !ok || cents == 0 {
		return Row{Check: checkBudget, Detail: "W is 0 or a model has no rate"}
	}
	if cents > WMaxCents {
		return Row{Check: checkBudget, Detail: fmt.Sprintf("W=%d exceeds 250", cents)}
	}
	inRate, outRate, inModel, outModel := ratePeaks(w.Price)
	return Row{Check: checkBudget, Pass: true, Detail: fmt.Sprintf(
		"W=%d max_in=%s (%s) max_out=%s (%s)", cents, inRate, inModel, outRate, outModel,
	)}
}

func checkP9(w World) Row {
	when := w.Now
	if when.IsZero() {
		when = time.Now()
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return Row{Check: checkModel, Detail: "PT zone"}
	}
	pt := when.In(loc)
	reset := time.Date(pt.Year(), pt.Month(), pt.Day(), 9, 8, 0, 0, loc)
	minutes := int(pt.Sub(reset).Minutes())
	if w.ModelStatus != 200 || !w.ModelListed {
		return Row{Check: checkModel, Detail: fmt.Sprintf("model HTTP %d PT %s minutes_since_0908=%d", w.ModelStatus, pt.Format(time.RFC3339), minutes)}
	}
	return Row{Check: checkModel, Pass: true, Detail: fmt.Sprintf("PT %s minutes_since_0908=%d", pt.Format(time.RFC3339), minutes)}
}

func checkP10(w World) Row {
	if w.PublishRepo != DreamingRepo {
		return Row{Check: checkPublish, Detail: "FACTORY_PUBLISH_DRAFT_REPO"}
	}
	for _, name := range w.SecretNames {
		if name == "GH_TOKEN" {
			return Row{Check: checkPublish, Detail: "GH_TOKEN listed"}
		}
	}
	if len(w.Purposes) == 0 {
		return Row{Check: checkPublish, Detail: "purpose missing"}
	}
	for _, purpose := range w.Purposes {
		if strings.TrimSpace(purpose) == "" {
			purpose = "work"
		}
		if purpose != "work" {
			return Row{Check: checkPublish, Detail: "purpose " + purpose}
		}
	}
	return Row{Check: checkPublish, Pass: true, Detail: "draft repo dreaming purpose work"}
}

func checkP11(w World) Row {
	if !strings.Contains(w.Workflow, "group: dreaming-live") || !strings.Contains(w.Workflow, "cancel-in-progress: false") {
		return Row{Check: checkKill, Detail: "concurrency"}
	}
	if !timeoutWithin(w.Workflow, 120) {
		return Row{Check: checkKill, Detail: "timeout-minutes"}
	}
	if !strings.Contains(w.Workflow, "exec ./spike/dreaming/run.sh") {
		return Row{Check: checkKill, Detail: "driver exec"}
	}
	if !strings.Contains(w.Workflow, "if: cancelled()") {
		return Row{Check: checkKill, Detail: "missing cleanup step"}
	}
	return Row{Check: checkKill, Pass: true, Detail: "concurrency dreaming-live cancel cleanup present"}
}

func checkP12(w World) Row {
	if w.Confirm != ConfirmValue {
		return Row{Check: checkGuards, Detail: "confirm"}
	}
	if w.Mode != "preflight" && w.Mode != "live" {
		return Row{Check: checkGuards, Detail: "mode"}
	}
	if strings.Contains(w.Workflow, "pull_request_target") || strings.Contains(w.Workflow, "m10-live.yml") {
		return Row{Check: checkGuards, Detail: "workflow trigger"}
	}
	if !strings.Contains(w.Workflow, "name: dreaming-live") {
		return Row{Check: checkGuards, Detail: "workflow name"}
	}
	switch w.Allowlist {
	case "pass":
	case "skip":
		return Row{Check: checkGuards, Detail: "TestPublisherRouteAllowlist skipped"}
	default:
		return Row{Check: checkGuards, Detail: "TestPublisherRouteAllowlist absent"}
	}
	return Row{Check: checkGuards, Pass: true, Detail: "confirm dreaming-live-454 allowlist passed"}
}

func checkP13(w World) Row {
	if !w.Reexec {
		return Row{Check: checkIsolation, Detail: fmt.Sprintf("no re-exec hits %d", w.SecretHits)}
	}
	if w.SecretHits != 0 {
		return Row{Check: checkIsolation, Detail: fmt.Sprintf("hits %d", w.SecretHits)}
	}
	return Row{Check: checkIsolation, Pass: true, Detail: "hits 0"}
}

func checkP14(w World) Row {
	if !w.AuthFormOK || !w.LsRemoteOK {
		return Row{Check: checkPrior, Detail: "ls-remote auth failed"}
	}
	for _, ref := range w.RemoteRefs {
		if strings.HasPrefix(ref, "refs/heads/"+RefPrefix) || strings.HasPrefix(ref, RefPrefix) {
			return Row{Check: checkPrior, Detail: "existing ref"}
		}
	}
	if w.PullsStatus != 200 {
		return Row{Check: checkPrior, Detail: fmt.Sprintf("GET /pulls HTTP %d", w.PullsStatus)}
	}
	for _, head := range w.PullHeads {
		if strings.HasPrefix(head, RefPrefix) {
			return Row{Check: checkPrior, Detail: "existing pr"}
		}
	}
	if w.DraftRows != 0 {
		return Row{Check: checkPrior, Detail: "draft_publication row"}
	}
	return Row{Check: checkPrior, Pass: true, Detail: "no factory/dreaming-454 ref or pull"}
}

func frozenTree(tree *definition.NativeTree) (string, bool) {
	if tree == nil {
		return "tree missing", false
	}
	byName := map[string]definition.NativePersona{}
	var root definition.NativePersona
	var haveRoot bool
	for _, persona := range tree.Personas {
		byName[persona.Name] = persona
		if persona.Name == "nils-aaberg" {
			root = persona
			haveRoot = true
		}
	}
	if !haveRoot {
		return "root missing", false
	}
	if root.ReportsTo != "" || len(root.SpawnableBy) != 0 {
		return "frozen root reports_to=" + root.ReportsTo, false
	}
	if root.SourceReportsTo != OutsideReport {
		return "source reports_to", false
	}
	if byName["beatriz-salgado"].ReportsTo != "nils-aaberg" || byName["viktor-halas"].ReportsTo != "beatriz-salgado" || byName["oskar-lindgren"].ReportsTo != "nils-aaberg" {
		return "seat reports_to", false
	}
	checked, err := definition.ValidateNativeTree(*tree)
	if err != nil || checked.Depth["viktor-halas"] != 2 || checked.Depth["oskar-lindgren"] != 1 {
		return "implementer or reviewer", false
	}
	names := []string{"nils-aaberg", "beatriz-salgado", "viktor-halas", "oskar-lindgren"}
	var edges []string
	for _, delegator := range names {
		for _, target := range names {
			if definition.AllowDelegate(byName[target], delegator, nil, false) == nil {
				edges = append(edges, delegator+"->"+target)
			}
		}
	}
	sort.Strings(edges)
	want := []string{"beatriz-salgado->viktor-halas", "nils-aaberg->beatriz-salgado", "nils-aaberg->oskar-lindgren"}
	if len(edges) != len(want) {
		return fmt.Sprintf("edges %d", len(edges)), false
	}
	for i := range want {
		if edges[i] != want[i] {
			return "edge " + edges[i], false
		}
	}
	return "frozen root empty source tobias-lindqvist edges 3", true
}

func workflowForbidden(text string) bool {
	return strings.Contains(text, "M1_OPENAI_EXTRA_HEADERS") || strings.Contains(text, "FACTORY_REVIEW_APP")
}

func timeoutWithin(text string, max int) bool {
	const key = "timeout-minutes:"
	i := strings.Index(text, key)
	if i < 0 {
		return false
	}
	rest := strings.TrimSpace(text[i+len(key):])
	n := 0
	seen := false
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		seen = true
		n = n*10 + int(c-'0')
	}
	return seen && n > 0 && n <= max
}

func scrubDetail(detail string, needles []string) string {
	for _, needle := range needles {
		if len(needle) < 8 {
			continue
		}
		detail = strings.ReplaceAll(detail, needle, "[redacted]")
	}
	return detail
}

func ratePeaks(price harness.SpendPrice) (inRate, outRate, inModel, outModel string) {
	var bestIn, bestOut float64
	for _, model := range price.Models {
		consider := func(raw string, input bool) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return
			}
			var value float64
			if _, err := fmt.Sscan(raw, &value); err != nil {
				return
			}
			if input {
				if inModel == "" || value > bestIn {
					bestIn = value
					inRate = raw
					inModel = model.Name
				}
				return
			}
			if outModel == "" || value > bestOut {
				bestOut = value
				outRate = raw
				outModel = model.Name
			}
		}
		consider(model.InputCostPerToken, true)
		consider(model.CacheCreationInputTokenCost, true)
		for _, extra := range model.InputAbove {
			consider(extra, true)
		}
		consider(model.OutputCostPerToken, false)
		for _, extra := range model.OutputAbove {
			consider(extra, false)
		}
	}
	return inRate, outRate, inModel, outModel
}

// WriteReport writes the JSON document.
func WriteReport(path string, report Report) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}
