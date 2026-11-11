package dreaminglive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/server"
)

// offlineD6C stands in for the D6c commit inside the injected secure world.
// A depth-1 clone still fails P2 because the ancestor object is absent.
const offlineD6C = "cccccccccccccccccccccccccccccccccccccccc"

// RunOffline evaluates every P1–P14 failure case and the secure case.
// Exit is nil only when each failure case fails on its own check and the
// secure case passes all fourteen.
func RunOffline(ctx context.Context, outPath string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dreaming-live.yml"))
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dreaming-offline-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tree, pin, blobs, err := BuildSynthetic(dir)
	if err != nil {
		return err
	}
	secure := secureWorld(string(workflow), tree, pin, dir, blobs)
	report := Evaluate(secure)
	if !report.Passed() {
		return fmt.Errorf("secure case: %s", firstFailure(report))
	}
	for _, tc := range failureCases(secure) {
		got := Evaluate(tc.world)
		row, ok := got.Row(tc.check)
		if !ok || row.Pass {
			return fmt.Errorf("%s: %s passed (%s)", tc.name, tc.check, row.Detail)
		}
	}
	if err := proveDepth1(secure); err != nil {
		return err
	}
	if err := proveBearer(ctx, secure); err != nil {
		return err
	}
	if outPath != "" {
		return WriteReport(outPath, report)
	}
	return nil
}

type failureCase struct {
	name  string
	check string
	world World
}

func failureCases(base World) []failureCase {
	secret := cloneWorld(base)
	secret.Secrets["M6_GITHUB_APP_PRIVATE_KEY"] = false
	headers := cloneWorld(base)
	headers.Workflow += "\nM1_OPENAI_EXTRA_HEADERS\n"
	closed := cloneWorld(base)
	closed.IssueOpen = false
	unknownPin := cloneWorld(base)
	unknownPin.PinExists = false
	blob := cloneWorld(base)
	blob.BlobGot["org/roster.yaml"] = strings.Repeat("ab", 20)
	unset := cloneWorld(base)
	unset.RoleSource = ""
	frozen := cloneWorld(base)
	mutated := *base.Tree
	mutated.Personas = append([]definition.NativePersona{}, base.Tree.Personas...)
	for i := range mutated.Personas {
		if mutated.Personas[i].Name == "nils-aaberg" {
			mutated.Personas[i].ReportsTo = OutsideReport
		}
	}
	frozen.Tree = &mutated
	unpriced := cloneWorld(base)
	unpriced.Price.Models = append([]harness.ModelRate{}, unpriced.Price.Models...)
	unpriced.Price.Models[1].OutputCostPerToken = ""
	missingList := cloneWorld(base)
	missingList.FallbackOK = false
	tooWide := cloneWorld(base)
	tooWide.Price = harness.SpendPrice{
		PromptByteCap: 398976,
		MaxTokens:     8000,
		Models:        []harness.ModelRate{{Name: "high", InputCostPerToken: "0.00006", OutputCostPerToken: "0.00025"}},
	}
	skipped := cloneWorld(base)
	skipped.Allowlist = "skip"
	noReexec := cloneWorld(base)
	noReexec.Reexec = false
	noReexec.SecretHits = 2
	existing := cloneWorld(base)
	existing.RemoteRefs = []string{"refs/heads/" + RefPrefix + "deadbeef"}
	denied := cloneWorld(base)
	denied.PullsStatus = http.StatusForbidden
	bearer := cloneWorld(base)
	bearer.AuthFormOK = false
	bearer.LsRemoteOK = false
	readOnly := cloneWorld(base)
	readOnly.AppPerms = map[string]string{"contents": "read", "metadata": "read"}
	missingApp := cloneWorld(base)
	missingApp.AppStatus = http.StatusNotFound
	noSentinel := cloneWorld(base)
	noSentinel.SentinelAtX = false
	noPackage := cloneWorld(base)
	noPackage.EvidencePackage = false
	ghToken := cloneWorld(base)
	ghToken.SecretNames = append([]string{}, base.SecretNames...)
	ghToken.SecretNames = append(ghToken.SecretNames, "GH_TOKEN")
	improve := cloneWorld(base)
	improve.Purposes = []string{"improvement"}
	cleanup := cloneWorld(base)
	cleanup.Workflow = strings.ReplaceAll(base.Workflow, "if: cancelled()", "if: failure()")
	model := cloneWorld(base)
	model.ModelListed = false
	model.ModelStatus = http.StatusNotFound
	wrongConfirm := cloneWorld(base)
	wrongConfirm.Confirm = "m10-live"
	return []failureCase{
		{name: "empty secret", check: checkSecrets, world: secret},
		{name: "extra headers", check: checkSecrets, world: headers},
		{name: "closed issue", check: checkRead, world: closed},
		{name: "unknown pin", check: checkRead, world: unknownPin},
		{name: "blob id", check: checkRoles, world: blob},
		{name: "role source unset", check: checkRoles, world: unset},
		{name: "frozen root", check: checkTree, world: frozen},
		{name: "unpriced fallback", check: checkBudget, world: unpriced},
		{name: "missing fallback list", check: checkBudget, world: missingList},
		{name: "W over 250", check: checkBudget, world: tooWide},
		{name: "allowlist skipped", check: checkGuards, world: skipped},
		{name: "no re-exec", check: checkIsolation, world: noReexec},
		{name: "existing ref", check: checkPrior, world: existing},
		{name: "pulls 403", check: checkPrior, world: denied},
		{name: "wrong auth form", check: checkPrior, world: bearer},
		{name: "read-only app", check: checkApp, world: readOnly},
		{name: "missing install", check: checkApp, world: missingApp},
		{name: "sentinel absent", check: checkEvidence, world: noSentinel},
		{name: "package missing", check: checkEvidence, world: noPackage},
		{name: "GH_TOKEN", check: checkPublish, world: ghToken},
		{name: "purpose improvement", check: checkPublish, world: improve},
		{name: "missing cleanup", check: checkKill, world: cleanup},
		{name: "model missing", check: checkModel, world: model},
		{name: "wrong confirm", check: checkGuards, world: wrongConfirm},
	}
}

func secureWorld(workflow string, tree definition.NativeTree, pin, dir string, blobs map[string]string) World {
	return World{
		Workflow: workflow,
		Secrets: map[string]bool{
			"M1_OPENAI_BASE_URL": true, "M1_OPENAI_API_KEY": true,
			"M6_GITHUB_APP_ID": true, "M6_GITHUB_APP_PRIVATE_KEY": true,
		},
		Head: tree.SourceCommit, FactorySHA: tree.SourceCommit, FetchDepthZero: true,
		Ancestors: map[string]bool{D6A: true, D6B: true, offlineD6C: true}, D6C: offlineD6C,
		AppStatus: 200, AppSlug: "m6-live", InstallationID: 7,
		AppPerms:        map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"},
		SelectionCovers: true,
		ReadTokenMinted: true,
		ReadPerms:       map[string]string{"contents": "read", "metadata": "read", "issues": "read", "pull_requests": "read"},
		ReadRepos:       []string{"dreaming"}, ReadTTL: 30 * time.Minute,
		PinExists: true, IssueOpen: true, IssueTitle: "DB-backed tenant isolation",
		DefaultBranch: "main", PinIsAncestor: true,
		RoleSource: dir, WantRoleSource: dir, CheckoutHead: pin, Pin: pin,
		BlobGot: blobs, BlobWant: blobs,
		Tree:            &tree,
		Tests:           map[string]string{"TestProducedHeadReviewerPipeline": "pass", "TestDraftPublishRequiresReviewerPass": "pass", "TestRunSpendHardStop": "pass", "TestHarnessSecretsNotInProc": "pass"},
		EvidencePackage: true, SentinelAtX: true,
		Price:       workedPrice(),
		FallbackOK:  true,
		ModelStatus: 200, ModelListed: true,
		PublishRepo: DreamingRepo,
		SecretNames: []string{"M6_GITHUB_APP_PRIVATE_KEY"},
		Purposes:    []string{"work", ""},
		Allowlist:   "pass",
		Confirm:     ConfirmValue, Mode: "preflight",
		Reexec: true, SecretHits: 0,
		LsRemoteOK: true, AuthFormOK: true, PullsStatus: 200,
	}
}

func workedPrice() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     8192,
		Models: []harness.ModelRate{
			{Name: ModelID, InputCostPerToken: "0.000003", OutputCostPerToken: "0.000015", CacheCreationInputTokenCost: "0.00000375"},
			{Name: "fallback", InputCostPerToken: "0.000003", OutputCostPerToken: "0.000015"},
		},
	}
}

func cloneWorld(in World) World {
	out := in
	out.Secrets = map[string]bool{}
	for k, v := range in.Secrets {
		out.Secrets[k] = v
	}
	out.Ancestors = map[string]bool{}
	for k, v := range in.Ancestors {
		out.Ancestors[k] = v
	}
	out.AppPerms = map[string]string{}
	for k, v := range in.AppPerms {
		out.AppPerms[k] = v
	}
	out.ReadPerms = map[string]string{}
	for k, v := range in.ReadPerms {
		out.ReadPerms[k] = v
	}
	out.BlobGot = map[string]string{}
	for k, v := range in.BlobGot {
		out.BlobGot[k] = v
	}
	out.BlobWant = map[string]string{}
	for k, v := range in.BlobWant {
		out.BlobWant[k] = v
	}
	out.Tests = map[string]string{}
	for k, v := range in.Tests {
		out.Tests[k] = v
	}
	return out
}

func proveDepth1(base World) error {
	origin, err := os.MkdirTemp("", "dreaming-origin-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(origin) }()
	if err := git(origin, "init", "-b", "main"); err != nil {
		return err
	}
	if err := git(origin, "config", "user.email", "dreaming@example.com"); err != nil {
		return err
	}
	if err := git(origin, "config", "user.name", "dreaming"); err != nil {
		return err
	}
	var first string
	for i := 0; i < 3; i++ {
		path := filepath.Join(origin, "f")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("n %d\n", i)), 0o644); err != nil {
			return err
		}
		if err := git(origin, "add", "f"); err != nil {
			return err
		}
		if err := git(origin, "commit", "-m", fmt.Sprintf("c%d", i)); err != nil {
			return err
		}
		if i == 0 {
			first, err = gitOut(origin, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
		}
	}
	shallow, err := os.MkdirTemp("", "dreaming-shallow-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(shallow) }()
	cmd := exec.Command("git", "clone", "--depth", "1", "file://"+origin, shallow)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("depth-1 clone: %s", out)
	}
	probe := exec.Command("git", "merge-base", "--is-ancestor", first, "HEAD")
	probe.Dir = shallow
	probe.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if err := probe.Run(); err == nil {
		return fmt.Errorf("depth-1 clone treated %s as an ancestor", first)
	}
	world := cloneWorld(base)
	world.Shallow = true
	delete(world.Ancestors, D6A)
	got := Evaluate(world)
	row, ok := got.Row(checkPin)
	if !ok || row.Pass || !strings.Contains(row.Detail, "ancestry unknown") {
		return fmt.Errorf("depth-1 P2: %+v", row)
	}
	return nil
}

func proveBearer(ctx context.Context, base World) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, server.GitBearerRejected+"\n")
	}))
	defer srv.Close()
	_, err := LsRemote(ctx, srv.URL+"/northstar-ai-labs/dreaming.git", "Authorization: Bearer not-a-basic-token", RefPrefix)
	if err == nil {
		return fmt.Errorf("bearer ls-remote succeeded")
	}
	world := cloneWorld(base)
	world.AuthFormOK = false
	world.LsRemoteOK = false
	row, _ := Evaluate(world).Row(checkPrior)
	if row.Pass {
		return fmt.Errorf("bearer P14 passed")
	}
	return nil
}

func firstFailure(report Report) string {
	for _, row := range report.Rows {
		if !row.Pass {
			return row.Check + " " + row.Detail
		}
	}
	return "none"
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}
