package dreaminglive

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/polds/factory/internal/harness"
)

// RunChecked is the preflight or live command. It writes preflight.json.
// mode=live calls RunLive only after every check passes.
func RunChecked(ctx context.Context, mode string) error {
	world, needles, err := gather(ctx, mode)
	if err != nil {
		return err
	}
	world.Needles = needles
	report := Evaluate(world)
	if err := WriteReport("preflight.json", report); err != nil {
		return err
	}
	// p14Hook is nil in the live binary. Tests set it. There is no env bypass.
	if !report.Passed() && !p14Allowed(report) {
		return fmt.Errorf("preflight: %s", firstFailure(report))
	}
	if mode == "live" {
		return runLive(ctx)
	}
	return nil
}

func gather(ctx context.Context, mode string) (World, []string, error) {
	root, err := moduleRoot()
	if err != nil {
		return World{}, nil, err
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dreaming-live.yml"))
	if err != nil {
		return World{}, nil, err
	}
	var needles []string
	for _, name := range []string{"M1_OPENAI_API_KEY", "M6_GITHUB_APP_PRIVATE_KEY"} {
		if v := strings.TrimSpace(os.Getenv(name)); len(v) >= 8 {
			needles = append(needles, v)
		}
	}
	world := World{
		Workflow: string(workflow),
		Secrets: map[string]bool{
			"M1_OPENAI_BASE_URL":        strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")) != "",
			"M1_OPENAI_API_KEY":         strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY")) != "",
			"M6_GITHUB_APP_ID":          strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")) != "",
			"M6_GITHUB_APP_PRIVATE_KEY": strings.TrimSpace(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY")) != "",
		},
		Confirm:     strings.TrimSpace(os.Getenv("CONFIRM")),
		Mode:        mode,
		PublishRepo: envDefault("FACTORY_PUBLISH_DRAFT_REPO", DreamingRepo),
		Purposes:    purposesFromConfig(),
		SecretNames: jobSecretNames(),
		Tests:       map[string]string{},
		Allowlist:   "absent",
		Price:       workedPrice(),
		FallbackOK:  true,
		Now:         time.Now(),
	}
	applyJobFile(&world)
	if err := fillGit(&world); err != nil {
		return World{}, nil, err
	}
	if err := fillGitHub(ctx, &world, &needles); err != nil {
		return World{}, nil, err
	}
	// The P4 token checkout lands before P5 reads the tree.
	if err := fillRole(&world); err != nil {
		return World{}, nil, err
	}
	if err := fillModel(ctx, &world); err != nil {
		return World{}, nil, err
	}
	world.DraftRows, err = draftRows(ctx)
	if err != nil {
		return World{}, nil, err
	}
	return world, needles, nil
}

func fillGit(world *World) error {
	dir := strings.TrimSpace(os.Getenv("FACTORY_GIT_DIR"))
	git := func(args ...string) *exec.Cmd {
		if dir != "" {
			args = append([]string{"-C", dir}, args...)
		}
		return exec.Command("git", args...)
	}
	head, err := git("rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	world.Head = strings.TrimSpace(string(head))
	world.FactorySHA = strings.TrimSpace(os.Getenv("FACTORY_SHA"))
	if world.FactorySHA == "" {
		world.FactorySHA = world.Head
	}
	shallow, err := git("rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		return err
	}
	world.Shallow = strings.TrimSpace(string(shallow)) == "true"
	world.FetchDepthZero = strings.Contains(world.Workflow, "fetch-depth: 0") && !world.Shallow
	d6c, err := git("rev-list", "-1", "HEAD", "--", "deploy/dreaming-live/preflight.go").Output()
	if err != nil {
		d6c = []byte{}
	}
	world.D6C = strings.TrimSpace(string(d6c))
	if raw := strings.TrimSpace(os.Getenv("FACTORY_REQUIRED_SHAS")); raw != "" && githubBase() != "https://api.github.com" {
		world.Required = strings.Split(raw, ",")
	}
	world.Ancestors = map[string]bool{}
	for _, sha := range requiredCommits(*world) {
		if sha == "" {
			continue
		}
		if git("cat-file", "-e", sha+"^{commit}").Run() != nil {
			continue
		}
		world.Ancestors[sha] = git("merge-base", "--is-ancestor", sha, "HEAD").Run() == nil
	}
	return nil
}

func fillGitHub(ctx context.Context, world *World, needles *[]string) error {
	appID, parseErr := strconv.ParseInt(strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")), 10, 64)
	key, keyErr := parseKey(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY"))
	if parseErr != nil || keyErr != nil || appID <= 0 {
		world.AppStatus = http.StatusUnauthorized
	}
	if world.AppStatus == http.StatusUnauthorized {
		return nil
	}
	inst, status, slug, perms, covers, installErr := installationDoc(ctx, appID, key)
	world.AppStatus = status
	if installErr == nil {
		world.InstallationID = inst
		world.AppSlug = slug
		world.AppPerms = perms
		world.SelectionCovers = covers
		token, mintErr := mintReadToken(ctx, key, appID, inst)
		if mintErr == nil {
			recordRead(ctx, world, needles, token)
		}
	}
	return nil
}

type mintedToken struct {
	Value  string
	Perms  map[string]string
	Repos  []string
	Expiry time.Time
}

func mintReadToken(ctx context.Context, key *rsa.PrivateKey, appID, inst int64) (mintedToken, error) {
	jwt, err := appJWT(appID, key)
	if err != nil {
		return mintedToken{}, err
	}
	body := []byte(`{"repositories":["dreaming"],"permissions":{"contents":"read","metadata":"read","issues":"read","pull_requests":"read"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubBase()+"/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return mintedToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return mintedToken{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var doc struct {
		Token       string            `json:"token"`
		ExpiresAt   string            `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
		Repos       []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return mintedToken{}, err
	}
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return mintedToken{}, fmt.Errorf("token HTTP %d", res.StatusCode)
	}
	when, err := time.Parse(time.RFC3339, doc.ExpiresAt)
	if err != nil {
		return mintedToken{}, err
	}
	var repos []string
	for _, repo := range doc.Repos {
		repos = append(repos, repo.Name)
	}
	return mintedToken{Value: doc.Token, Perms: doc.Permissions, Repos: repos, Expiry: when}, nil
}

func recordRead(ctx context.Context, world *World, needles *[]string, tok mintedToken) {
	if len(tok.Value) >= 8 {
		*needles = append(*needles, tok.Value)
	}
	world.ReadTokenMinted = true
	world.ReadPerms = tok.Perms
	world.ReadRepos = tok.Repos
	world.ReadTTL = time.Until(tok.Expiry)
	if world.ReadTTL > time.Hour {
		world.ReadTTL = time.Hour
	}
	fillReadFacts(ctx, world, tok.Value)
	header, headerErr := AuthHeader(tok.Value)
	world.AuthFormOK = headerErr == nil && strings.HasPrefix(header, "Authorization: Basic ")
	if headerErr != nil {
		return
	}
	remote := dreamingGitRemote()
	// A directory already at the pin is left alone. Otherwise this is the
	// workflow checkout into FACTORY_ROLE_SOURCE (RUNNER_TEMP/dreaming-src).
	// The token stays in http.extraheader. The directory is not uploaded.
	// A failed checkout leaves the directory off the pin. P5 then fails closed.
	_ = checkoutDreaming(ctx, header, remote, strings.TrimSpace(os.Getenv("FACTORY_ROLE_SOURCE")), dreamingPin())
	refs, remoteErr := LsRemote(ctx, remote, header, RefPrefix)
	world.LsRemoteOK = remoteErr == nil
	if remoteErr == nil {
		world.RemoteRefs = refs
	}
	status, heads := listPullHeads(ctx, tok.Value)
	world.PullsStatus = status
	world.PullHeads = heads
}

func listPullHeads(ctx context.Context, token string) (int, []string) {
	var heads []string
	for page := 1; page <= 50; page++ {
		path := fmt.Sprintf("/repos/%s/pulls?state=all&per_page=100&page=%d", DreamingRepo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubBase()+path, nil)
		if err != nil {
			return http.StatusBadRequest, nil
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return http.StatusBadGateway, nil
		}
		var batch []struct {
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&batch)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return res.StatusCode, nil
		}
		if decodeErr != nil {
			return res.StatusCode, nil
		}
		for _, item := range batch {
			if strings.HasPrefix(item.Head.Ref, RefPrefix) {
				heads = append(heads, item.Head.Ref)
			}
		}
		if len(batch) < 100 {
			return http.StatusOK, heads
		}
	}
	return http.StatusOK, heads
}

func fillReadFacts(ctx context.Context, world *World, token string) {
	issue, status := getJSON(ctx, "/repos/"+DreamingRepo+"/issues/454", token)
	world.IssueOpen = status == http.StatusOK && stringField(issue, "state") == "open"
	world.IssueTitle = stringField(issue, "title")
	repo, repoStatus := getJSON(ctx, "/repos/"+DreamingRepo, token)
	if repoStatus == http.StatusOK {
		world.DefaultBranch = stringField(repo, "default_branch")
	}
	pin := dreamingPin()
	commitStatus := httpStatus(ctx, "/repos/"+DreamingRepo+"/commits/"+pin, token)
	world.PinExists = commitStatus == http.StatusOK
	if world.DefaultBranch != "" {
		cmp, cmpStatus := getJSON(ctx, "/repos/"+DreamingRepo+"/compare/"+pin+"..."+world.DefaultBranch, token)
		state := stringField(cmp, "status")
		world.PinIsAncestor = cmpStatus == http.StatusOK && (state == "ahead" || state == "identical")
	}
	world.Pin = pin
}

func fillModel(ctx context.Context, world *World) error {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")), "/")
	key := strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY"))
	if base == "" || key == "" {
		world.ModelStatus = http.StatusUnauthorized
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-litellm-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		world.ModelStatus = http.StatusBadGateway
		return nil
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	world.ModelStatus = res.StatusCode
	world.ModelListed = strings.Contains(string(body), ModelID)
	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/model/info", nil)
	if err != nil {
		return err
	}
	infoReq.Header.Set("x-litellm-api-key", key)
	infoRes, err := http.DefaultClient.Do(infoReq)
	if err != nil {
		world.FallbackOK = false
		return nil
	}
	defer func() { _ = infoRes.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(infoRes.Body, 1<<20))
	price, ok := priceFromModelInfo(raw)
	world.FallbackOK = ok
	if ok {
		world.Price = price
	}
	return nil
}

func draftRows(ctx context.Context) (int, error) {
	dsn := strings.TrimSpace(os.Getenv("FACTORY_DATABASE_URL"))
	if dsn == "" {
		return 0, nil
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM draft_publications`).Scan(&n)
	if err == nil {
		return n, nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42P01" {
		return 0, nil
	}
	return 0, err
}

// priceFromModelInfo reads GET /model/info.
// Rates are USD per token in model_info (external-api-facts: input_cost_per_token,
// output_cost_per_token, cache_creation_input_token_cost, above-N tiers).
// The fallback list is litellm_params.fallbacks. When that key is absent, the
// same list is read from router_settings.fallbacks, which LiteLLM stores as
// [{model: [fallback, ...]}]. A missing list from both places fails closed.
func priceFromModelInfo(raw []byte) (harness.SpendPrice, bool) {
	var doc struct {
		Data []struct {
			ModelName string `json:"model_name"`
			Params    struct {
				Fallbacks []string `json:"fallbacks"`
			} `json:"litellm_params"`
			Info map[string]json.RawMessage `json:"model_info"`
		} `json:"data"`
		Router struct {
			Fallbacks []map[string][]string `json:"fallbacks"`
		} `json:"router_settings"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return harness.SpendPrice{}, false
	}
	byName := map[string]int{}
	for i, item := range doc.Data {
		byName[item.ModelName] = i
	}
	primary, ok := byName[ModelID]
	if !ok {
		return harness.SpendPrice{}, false
	}
	fallbacks := doc.Data[primary].Params.Fallbacks
	if fallbacks == nil {
		fallbacks = routerFallbacks(doc.Router.Fallbacks, ModelID)
	}
	if fallbacks == nil {
		return harness.SpendPrice{}, false
	}
	names := append([]string{ModelID}, fallbacks...)
	price := workedPrice()
	price.Models = nil
	for _, name := range names {
		idx, found := byName[name]
		if !found {
			return harness.SpendPrice{}, false
		}
		info := doc.Data[idx].Info
		model := harness.ModelRate{Name: name}
		model.InputCostPerToken = rawNumber(info["input_cost_per_token"])
		model.OutputCostPerToken = rawNumber(info["output_cost_per_token"])
		model.CacheCreationInputTokenCost = rawNumber(info["cache_creation_input_token_cost"])
		if model.InputCostPerToken == "" || model.OutputCostPerToken == "" {
			return harness.SpendPrice{}, false
		}
		for key, value := range info {
			if strings.Contains(key, "above") && strings.Contains(key, "input") {
				model.InputAbove = append(model.InputAbove, rawNumber(value))
			}
			if strings.Contains(key, "above") && strings.Contains(key, "output") {
				model.OutputAbove = append(model.OutputAbove, rawNumber(value))
			}
		}
		price.Models = append(price.Models, model)
	}
	return price, true
}

func routerFallbacks(rows []map[string][]string, model string) []string {
	for _, row := range rows {
		if names, ok := row[model]; ok {
			return names
		}
	}
	return nil
}

func rawNumber(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	return strings.Trim(text, `"`)
}

func installationDoc(ctx context.Context, appID int64, key *rsa.PrivateKey) (int64, int, string, map[string]string, bool, error) {
	jwt, err := appJWT(appID, key)
	if err != nil {
		return 0, http.StatusUnauthorized, "", nil, false, err
	}
	doc, status := getJSON(ctx, "/repos/"+DreamingRepo+"/installation", jwt)
	if status != http.StatusOK {
		return 0, status, "", nil, false, nil
	}
	var parsed struct {
		ID          int64             `json:"id"`
		Slug        string            `json:"app_slug"`
		Selection   string            `json:"repository_selection"`
		Permissions map[string]string `json:"permissions"`
	}
	raw, _ := json.Marshal(doc)
	if json.Unmarshal(raw, &parsed) != nil || parsed.ID == 0 {
		return 0, status, "", nil, false, errors.New("installation document")
	}
	covers := parsed.Selection == "all" || parsed.Selection == "selected"
	return parsed.ID, status, parsed.Slug, parsed.Permissions, covers, nil
}

func appJWT(appID int64, key *rsa.PrivateKey) (string, error) {
	now := time.Now()
	header, err := b64(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := b64(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func dreamingGitRemote() string {
	if githubBase() == "https://api.github.com" {
		return "https://github.com/" + DreamingRepo + ".git"
	}
	return strings.TrimRight(githubBase(), "/") + "/northstar-ai-labs/dreaming.git"
}

func githubBase() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("FACTORY_GITHUB_API_BASE")), "/")
	if base == "" {
		return "https://api.github.com"
	}
	return base
}

func getJSON(ctx context.Context, path, auth string) (map[string]any, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubBase()+path, nil)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "factory")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway
	}
	defer func() { _ = res.Body.Close() }()
	var doc map[string]any
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc)
	return doc, res.StatusCode
}

func httpStatus(ctx context.Context, path, auth string) int {
	_, status := getJSON(ctx, path, auth)
	return status
}

func stringField(doc map[string]any, key string) string {
	if doc == nil {
		return ""
	}
	v, _ := doc[key].(string)
	return v
}

func parseKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("app key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("app key")
	}
	return key, nil
}

// p14Hook is assigned only by tests. The live command never sets it, and it
// does not read an environment variable.
var p14Hook func(Report) bool

func p14Allowed(report Report) bool {
	if p14Hook == nil {
		return false
	}
	return p14Hook(report)
}

func packageWired(root, pkg string) bool {
	dir := filepath.Join(root, filepath.FromSlash(pkg))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		text := string(body)
		if strings.Contains(text, "database/sql") || strings.Contains(text, "github.com/jackc/pgx") || strings.Contains(text, "DREAMD_TEST_DSN") {
			return true
		}
	}
	return false
}

func p14Only(report Report) bool {
	saw := false
	for _, row := range report.Rows {
		if row.Check == checkPrior {
			saw = !row.Pass
			continue
		}
		if !row.Pass {
			return false
		}
	}
	return saw
}

func dreamingPin() string {
	if pin := strings.TrimSpace(os.Getenv("DREAMING_PIN")); len(pin) == 40 {
		return pin
	}
	return DreamingPin
}

func purposesFromConfig() []string {
	seats, err := LoadSeats()
	if err != nil || seats.Purpose == "" {
		return []string{"work"}
	}
	return []string{seats.Purpose}
}

func jobSecretNames() []string {
	raw := strings.TrimSpace(os.Getenv("FACTORY_SECRET_NAMES"))
	var names []string
	if raw != "" {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func applyJobFile(world *World) {
	path := strings.TrimSpace(os.Getenv("PREFLIGHT_JOB_FILE"))
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc struct {
		Tests     map[string]string `json:"tests"`
		Allowlist string            `json:"allowlist"`
		Reexec    bool              `json:"reexec"`
		Hits      int               `json:"hits"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	if doc.Tests != nil {
		world.Tests = doc.Tests
	}
	if doc.Allowlist != "" {
		world.Allowlist = doc.Allowlist
	}
	world.Reexec = doc.Reexec
	world.SecretHits = doc.Hits
}

func fillRole(world *World) error {
	dir := strings.TrimSpace(os.Getenv("FACTORY_ROLE_SOURCE"))
	world.WantRoleSource = dir
	world.RoleSource = dir
	if dir == "" {
		return nil
	}
	tree, pin, blobs, loadErr := LoadTree(dir)
	if loadErr == nil {
		world.Tree = &tree
		world.CheckoutHead = pin
		world.BlobGot = blobs
		var want map[string]string
		if raw := strings.TrimSpace(os.Getenv("ROLE_BLOBS")); raw != "" {
			_ = json.Unmarshal([]byte(raw), &want)
		}
		world.BlobWant = want
		pkg := configuredPackage()
		info, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(pkg)))
		world.EvidencePackage = statErr == nil && info.IsDir()
		world.DBWired = packageWired(dir, pkg)
		mut := configuredMutation()
		goal, _ := LoadGoal()
		world.MutationOK = harness.EvidenceMutationPath(mut) == nil && mut != "" && strings.Contains(goal, "FACTORY_EVIDENCE_MUTATION")
	}
	return nil
}

// runLiveChain is the mode=live continuation. Tests replace it when they
// only need to observe that a passing preflight reaches the chain.
var runLiveChain = bootAndRunChain

func runLive(ctx context.Context) error {
	return runLiveChain(ctx)
}

func configuredPackage() string {
	return strings.TrimPrefix(envDefault("FACTORY_EVIDENCE_PACKAGE", "oracle"), "./")
}

func configuredMutation() string {
	return envDefault("FACTORY_EVIDENCE_MUTATION", configuredPackage()+"/tenant_test.go")
}

func envDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
